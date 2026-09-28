package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

// Event reasons the load balancer records on Services.
const (
	EventSingleNodeLoadBalancer = "SingleNodeLoadBalancer"
	EventUnsupportedPort        = "UnsupportedPort"
	EventImmutableSetting       = "ImmutableSetting"
)

const (
	loadBalancerStateDeleting = "deleting"
	edgeRoleLoadBalancer      = "load_balancer"
	edgePlacementCombined     = "combined"
	defaultEdgeMemberWeight   = 100
	zoneLabel                 = "topology.kubernetes.io/zone"
)

// LoadBalancers implements cloudprovider.LoadBalancer: one Ankra load balancer per Service, or the Service's
// members on a combined edge's load_balancer role.
type LoadBalancers struct {
	api     cloudapi.API
	options Options

	recorderLock sync.RWMutex
	recorder     record.EventRecorder
}

// NewLoadBalancers builds the Service load balancer implementation.
func NewLoadBalancers(api cloudapi.API, options Options) *LoadBalancers {
	return &LoadBalancers{api: api, options: options}
}

// SetRecorder sets where events on Services go.
func (balancers *LoadBalancers) SetRecorder(recorder record.EventRecorder) {
	balancers.recorderLock.Lock()
	defer balancers.recorderLock.Unlock()
	balancers.recorder = recorder
}

func (balancers *LoadBalancers) event(service *v1.Service, eventType string, reason string, messageFormat string, arguments ...any) {
	balancers.recorderLock.RLock()
	recorder := balancers.recorder
	balancers.recorderLock.RUnlock()
	if recorder != nil {
		recorder.Eventf(service, eventType, reason, messageFormat, arguments...)
	}
}

// servicePort is a Service port the load balancer forwards: its frontend port and the nodes' NodePort.
type servicePort struct {
	name     string
	port     int64
	nodePort int64
}

// desiredPorts lists the Service's TCP ports; other protocols are returned as skipped (HAProxy balances TCP).
func desiredPorts(service *v1.Service) ([]servicePort, []string, error) {
	var ports []servicePort
	var skipped []string
	for _, port := range service.Spec.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = v1.ProtocolTCP
		}
		if protocol != v1.ProtocolTCP {
			skipped = append(skipped, fmt.Sprintf("%d/%s", port.Port, protocol))
			continue
		}
		if port.NodePort == 0 {
			return nil, nil, fmt.Errorf("port %d has no NodePort; the Ankra load balancer forwards to NodePorts, so allocateLoadBalancerNodePorts must not be false", port.Port)
		}
		ports = append(ports, servicePort{name: listenerName(port.Port), port: int64(port.Port), nodePort: int64(port.NodePort)})
	}
	return ports, skipped, nil
}

// serviceFamilies is the Service's IP families, IPv6 first; both when the Service does not say.
func serviceFamilies(service *v1.Service) []v1.IPFamily {
	families := slices.Clone(service.Spec.IPFamilies)
	if len(families) == 0 {
		families = []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol}
	}
	sort.SliceStable(families, func(left int, right int) bool {
		return families[left] == v1.IPv6Protocol && families[right] != v1.IPv6Protocol
	})
	return families
}

// nodeAddress picks the node address a load balancer reaches of one family. On a private network the private
// address (the ULA or RFC1918 leg) comes before the public one; without one only public addresses serve.
func nodeAddress(node *v1.Node, family v1.IPFamily, isOnNetwork bool) string {
	order := []v1.NodeAddressType{v1.NodeExternalIP}
	if isOnNetwork {
		order = []v1.NodeAddressType{v1.NodeInternalIP, v1.NodeExternalIP}
	}
	for _, addressType := range order {
		for _, address := range node.Status.Addresses {
			if address.Type != addressType {
				continue
			}
			parsed, parseError := netip.ParseAddr(address.Address)
			if parseError != nil {
				continue
			}
			if (family == v1.IPv6Protocol) == isIPv6(parsed) {
				return parsed.String()
			}
		}
	}
	return ""
}

// desiredMembers is one member per node and Service family (IPv6 first) at the port's NodePort.
func desiredMembers(service *v1.Service, nodes []*v1.Node, port servicePort, isOnNetwork bool) []cloudapi.Member {
	sortedNodes := slices.Clone(nodes)
	sort.Slice(sortedNodes, func(left int, right int) bool { return sortedNodes[left].Name < sortedNodes[right].Name })
	var members []cloudapi.Member
	for _, family := range serviceFamilies(service) {
		for _, node := range sortedNodes {
			address := nodeAddress(node, family, isOnNetwork)
			if address == "" {
				continue
			}
			members = append(members, cloudapi.Member{Name: memberName(node.Name, address), Address: address, Port: port.nodePort})
		}
	}
	return members
}

func sameMembers(current []cloudapi.Member, desired []cloudapi.Member) bool {
	key := func(member cloudapi.Member) string {
		return fmt.Sprintf("%s|%s|%d", member.Name, member.Address, member.Port)
	}
	currentKeys := make([]string, 0, len(current))
	for _, member := range current {
		currentKeys = append(currentKeys, key(member))
	}
	desiredKeys := make([]string, 0, len(desired))
	for _, member := range desired {
		desiredKeys = append(desiredKeys, key(member))
	}
	slices.Sort(currentKeys)
	slices.Sort(desiredKeys)
	return slices.Equal(currentKeys, desiredKeys)
}

func ingressStatus(publicIPv6 string, publicIPv4 string) *v1.LoadBalancerStatus {
	status := &v1.LoadBalancerStatus{}
	if publicIPv6 != "" {
		status.Ingress = append(status.Ingress, v1.LoadBalancerIngress{IP: publicIPv6})
	}
	if publicIPv4 != "" {
		status.Ingress = append(status.Ingress, v1.LoadBalancerIngress{IP: publicIPv4})
	}
	return status
}

func (balancers *LoadBalancers) serviceLabels(clusterName string, service *v1.Service) map[string]string {
	return map[string]string{LabelService: serviceKey(service), LabelCluster: clusterName}
}

// findLoadBalancer finds the Service's dedicated load balancer: by its labels, else (a create whose labelling did
// not finish) by its deterministic name when it carries no service label. nil when there is none.
func (balancers *LoadBalancers) findLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*cloudapi.LoadBalancer, error) {
	all, listError := balancers.api.ListLoadBalancers(ctx)
	if listError != nil {
		return nil, fmt.Errorf("list load balancers: %w", listError)
	}
	key := serviceKey(service)
	for index := range all {
		labels := all[index].Labels
		if labels[LabelService] == key && labels[LabelCluster] == clusterName {
			return &all[index], nil
		}
	}
	name := loadBalancerName(clusterName, service)
	for index := range all {
		if all[index].Name == name && all[index].Labels[LabelService] == "" {
			return &all[index], nil
		}
	}
	return nil, nil
}

// GetLoadBalancerName is the dedicated load balancer's name.
func (balancers *LoadBalancers) GetLoadBalancerName(_ context.Context, clusterName string, service *v1.Service) string {
	return loadBalancerName(clusterName, service)
}

// GetLoadBalancer reports whether the Service has its load balancer and its addresses.
func (balancers *LoadBalancers) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	if edgeID := strings.TrimSpace(service.Annotations[AnnotationEdge]); edgeID != "" {
		edge, edgeError := balancers.api.GetEdge(ctx, edgeID)
		if errors.Is(edgeError, cloudapi.ErrNotFound) {
			return nil, false, nil
		}
		if edgeError != nil {
			return nil, false, fmt.Errorf("get edge %s: %w", edgeID, edgeError)
		}
		prefix := edgeMemberPrefix(clusterName, service)
		hasMembers := slices.ContainsFunc(edge.Members, func(member cloudapi.EdgeMember) bool { return strings.HasPrefix(member.Name, prefix) })
		if !hasMembers {
			return nil, false, nil
		}
		return edgeStatus(edge), true, nil
	}
	balancer, findError := balancers.findLoadBalancer(ctx, clusterName, service)
	if findError != nil || balancer == nil {
		return nil, false, findError
	}
	return ingressStatus(balancer.PublicIPv6, balancer.PublicIPv4), true, nil
}

// EnsureLoadBalancer creates or updates the Service's load balancer and returns its addresses.
func (balancers *LoadBalancers) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	settings, settingsError := parseServiceSettings(service, balancers.options)
	if settingsError != nil {
		return nil, settingsError
	}
	ports, skipped, portsError := desiredPorts(service)
	if portsError != nil {
		return nil, portsError
	}
	if len(skipped) > 0 {
		balancers.event(service, v1.EventTypeWarning, EventUnsupportedPort, "Ankra load balancers forward TCP only; not forwarding %s", strings.Join(skipped, ", "))
	}
	if settings.edgeID != "" {
		status, edgeError := balancers.ensureEdge(ctx, clusterName, service, nodes, settings, ports)
		if edgeError != nil {
			return nil, edgeError
		}
		if deleteError := balancers.deleteDedicated(ctx, clusterName, service); deleteError != nil {
			return nil, deleteError
		}
		return status, nil
	}
	existing, findError := balancers.findLoadBalancer(ctx, clusterName, service)
	if findError != nil {
		return nil, findError
	}
	if existing == nil {
		created, createError := balancers.create(ctx, clusterName, service, nodes, settings)
		if createError != nil {
			return nil, createError
		}
		existing = &created
	} else if existing.Labels[LabelService] == "" {
		labels := maps.Clone(existing.Labels)
		if labels == nil {
			labels = map[string]string{}
		}
		maps.Copy(labels, balancers.serviceLabels(clusterName, service))
		if updateError := balancers.api.UpdateLoadBalancer(ctx, existing.ID, cloudapi.UpdateLoadBalancerInput{Labels: labels}); updateError != nil {
			return nil, fmt.Errorf("label load balancer %s: %w", existing.ID, updateError)
		}
	}
	balancer, getError := balancers.api.GetLoadBalancer(ctx, existing.ID)
	if getError != nil {
		return nil, fmt.Errorf("get load balancer %s: %w", existing.ID, getError)
	}
	if balancer.State == loadBalancerStateDeleting {
		return nil, fmt.Errorf("load balancer %s is being deleted; retrying once it is gone", balancer.ID)
	}
	balancers.reportImmutableDrift(service, balancer, settings)
	if reconcileError := balancers.reconcileListeners(ctx, service, nodes, balancer, ports, settings); reconcileError != nil {
		return nil, reconcileError
	}
	if balancer.PublicIPv6 == "" && balancer.PublicIPv4 == "" {
		return nil, fmt.Errorf("load balancer %s has no address yet (state %s)", balancer.ID, balancer.State)
	}
	return ingressStatus(balancer.PublicIPv6, balancer.PublicIPv4), nil
}

// zoneOf is where a new load balancer goes: the annotation or ANKRA_CLOUD_ZONE, else the first node's zone.
func zoneOf(settings serviceSettings, nodes []*v1.Node) string {
	if settings.zone != "" {
		return settings.zone
	}
	sortedNodes := slices.Clone(nodes)
	sort.Slice(sortedNodes, func(left int, right int) bool { return sortedNodes[left].Name < sortedNodes[right].Name })
	for _, node := range sortedNodes {
		if zone := node.Labels[zoneLabel]; zone != "" {
			return zone
		}
		if identifier, parseError := ParseProviderID(node.Spec.ProviderID); parseError == nil {
			return identifier.Zone
		}
	}
	return ""
}

// highAvailabilityFor decides whether a new load balancer is an HA pair: always or never when configured, else
// what the zone's growth stage allows (a zone with one compute node runs a single VM).
func (balancers *LoadBalancers) highAvailabilityFor(ctx context.Context, zone string) (bool, string, error) {
	switch balancers.options.HighAvailability {
	case HighAvailabilityAlways:
		return true, "", nil
	case HighAvailabilityNever:
		return false, HighAvailabilityEnvironmentVariable + " is false", nil
	}
	capabilities, capabilitiesError := balancers.api.GetZoneCapabilities(ctx, zone)
	if capabilitiesError != nil {
		return false, "", fmt.Errorf("get the capabilities of zone %s: %w", zone, capabilitiesError)
	}
	if !capabilities.IsKnown || capabilities.LoadBalancerHA {
		return true, "", nil
	}
	return false, fmt.Sprintf("zone %s is at the %q stage and cannot place a load balancer pair on different compute nodes", zone, capabilities.Stage), nil
}

func (balancers *LoadBalancers) create(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node, settings serviceSettings) (cloudapi.LoadBalancer, error) {
	zone := zoneOf(settings, nodes)
	if zone == "" {
		return cloudapi.LoadBalancer{}, fmt.Errorf("no zone for the load balancer: set the %s annotation or %s, or label the nodes with %s", AnnotationZone, ZoneEnvironmentVariable, zoneLabel)
	}
	isHighlyAvailable, reason, availabilityError := balancers.highAvailabilityFor(ctx, zone)
	if availabilityError != nil {
		return cloudapi.LoadBalancer{}, availabilityError
	}
	name := loadBalancerName(clusterName, service)
	created, createError := balancers.api.CreateLoadBalancer(ctx, cloudapi.CreateLoadBalancerInput{
		Name: name, Zone: zone, NetworkID: settings.networkID, Labels: balancers.serviceLabels(clusterName, service),
		HighAvailability: isHighlyAvailable, PublicIPv4: settings.wantsIPv4,
	})
	if createError != nil {
		return cloudapi.LoadBalancer{}, fmt.Errorf("create load balancer %s: %w", name, createError)
	}
	klog.InfoS("Created load balancer", "service", klog.KObj(service), "loadBalancer", created.ID, "zone", zone,
		"highAvailability", isHighlyAvailable, "publicIPv4", settings.wantsIPv4)
	if !isHighlyAvailable {
		klog.InfoS("Load balancer runs as a single VM without failover", "service", klog.KObj(service), "loadBalancer", created.ID, "reason", reason)
		balancers.event(service, v1.EventTypeNormal, EventSingleNodeLoadBalancer,
			"Load balancer %s runs as a single VM without failover (high_availability: false): %s. It stays single when the zone grows; recreate the Service to get a pair.", created.ID, reason)
	}
	return created, nil
}

// reportImmutableDrift explains on the Service the settings that only apply when the load balancer is created.
func (balancers *LoadBalancers) reportImmutableDrift(service *v1.Service, balancer cloudapi.LoadBalancer, settings serviceSettings) {
	if settings.networkID != "" && balancer.NetworkID != "" && settings.networkID != balancer.NetworkID {
		balancers.event(service, v1.EventTypeWarning, EventImmutableSetting,
			"Load balancer %s is on network %s, not %s; the network is chosen at creation, so recreate the Service to move it", balancer.ID, balancer.NetworkID, settings.networkID)
	}
	if settings.wantsIPv4 && balancer.PublicIPv4 == "" && balancer.State == "running" {
		balancers.event(service, v1.EventTypeWarning, EventImmutableSetting,
			"Load balancer %s has no IPv4 address; %s is honoured at creation, so recreate the Service to add IPv4", balancer.ID, AnnotationIPv4)
	}
}

// reconcileListeners makes the load balancer's frontends, backends, health checks and members match the Service:
// one tcp-<port> backend and frontend per port, members from the nodes. Stale frontends go before stale backends.
func (balancers *LoadBalancers) reconcileListeners(ctx context.Context, service *v1.Service, nodes []*v1.Node, balancer cloudapi.LoadBalancer, ports []servicePort, settings serviceSettings) error {
	desiredNames := map[string]bool{}
	for _, port := range ports {
		desiredNames[port.name] = true
	}
	frontendsByName := map[string]cloudapi.Frontend{}
	for _, frontend := range balancer.Frontends {
		if !desiredNames[frontend.Name] {
			if deleteError := balancers.api.DeleteFrontend(ctx, balancer.ID, frontend.ID); deleteError != nil && !errors.Is(deleteError, cloudapi.ErrNotFound) {
				return fmt.Errorf("delete frontend %s: %w", frontend.Name, deleteError)
			}
			continue
		}
		frontendsByName[frontend.Name] = frontend
	}
	backendsByName := map[string]cloudapi.Backend{}
	for _, backend := range balancer.Backends {
		backendsByName[backend.Name] = backend
	}
	isOnNetwork := balancer.NetworkID != "" || settings.networkID != ""
	for _, port := range ports {
		backend, hasBackend := backendsByName[port.name]
		input := cloudapi.BackendInput{Name: port.name, Mode: "tcp", HealthCheck: settings.healthCheck}
		if !hasBackend {
			created, createError := balancers.api.CreateBackend(ctx, balancer.ID, input)
			if createError != nil {
				return fmt.Errorf("create backend %s: %w", port.name, createError)
			}
			backend = created
		} else if backend.HealthCheck != settings.healthCheck {
			if updateError := balancers.api.UpdateBackend(ctx, balancer.ID, backend.ID, input); updateError != nil {
				return fmt.Errorf("update the health check of backend %s: %w", port.name, updateError)
			}
		}
		members := desiredMembers(service, nodes, port, isOnNetwork)
		if !sameMembers(backend.Members, members) {
			if replaceError := balancers.api.ReplaceMembers(ctx, balancer.ID, backend.ID, members); replaceError != nil {
				return fmt.Errorf("replace the members of backend %s: %w", port.name, replaceError)
			}
			klog.InfoS("Replaced load balancer members", "service", klog.KObj(service), "loadBalancer", balancer.ID, "backend", port.name, "members", len(members))
		}
		frontend, hasFrontend := frontendsByName[port.name]
		if hasFrontend && (frontend.Port != port.port || frontend.BackendID != backend.ID) {
			if deleteError := balancers.api.DeleteFrontend(ctx, balancer.ID, frontend.ID); deleteError != nil && !errors.Is(deleteError, cloudapi.ErrNotFound) {
				return fmt.Errorf("delete frontend %s: %w", frontend.Name, deleteError)
			}
			hasFrontend = false
		}
		if !hasFrontend {
			if _, createError := balancers.api.CreateFrontend(ctx, balancer.ID, cloudapi.FrontendInput{Name: port.name, Port: port.port, BackendID: backend.ID}); createError != nil {
				return fmt.Errorf("create frontend %s: %w", port.name, createError)
			}
		}
	}
	for _, backend := range balancer.Backends {
		if desiredNames[backend.Name] {
			continue
		}
		if deleteError := balancers.api.DeleteBackend(ctx, balancer.ID, backend.ID); deleteError != nil && !errors.Is(deleteError, cloudapi.ErrNotFound) {
			return fmt.Errorf("delete backend %s: %w", backend.Name, deleteError)
		}
	}
	return nil
}

// UpdateLoadBalancer brings the members up to date with the nodes (and anything else that drifted).
func (balancers *LoadBalancers) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	settings, settingsError := parseServiceSettings(service, balancers.options)
	if settingsError != nil {
		return settingsError
	}
	ports, _, portsError := desiredPorts(service)
	if portsError != nil {
		return portsError
	}
	if settings.edgeID != "" {
		_, edgeError := balancers.ensureEdge(ctx, clusterName, service, nodes, settings, ports)
		return edgeError
	}
	existing, findError := balancers.findLoadBalancer(ctx, clusterName, service)
	if findError != nil {
		return findError
	}
	if existing == nil {
		return fmt.Errorf("the load balancer of service %s does not exist yet", serviceKey(service))
	}
	balancer, getError := balancers.api.GetLoadBalancer(ctx, existing.ID)
	if getError != nil {
		return fmt.Errorf("get load balancer %s: %w", existing.ID, getError)
	}
	return balancers.reconcileListeners(ctx, service, nodes, balancer, ports, settings)
}

// deleteDedicated deletes the Service's dedicated load balancer if it has one; already gone is success.
func (balancers *LoadBalancers) deleteDedicated(ctx context.Context, clusterName string, service *v1.Service) error {
	existing, findError := balancers.findLoadBalancer(ctx, clusterName, service)
	if findError != nil {
		return findError
	}
	if existing == nil || existing.State == loadBalancerStateDeleting {
		return nil
	}
	deleteError := balancers.api.DeleteLoadBalancer(ctx, existing.ID)
	if deleteError != nil && !errors.Is(deleteError, cloudapi.ErrNotFound) {
		return fmt.Errorf("delete load balancer %s: %w", existing.ID, deleteError)
	}
	klog.InfoS("Deleted load balancer", "service", klog.KObj(service), "loadBalancer", existing.ID)
	return nil
}

// EnsureLoadBalancerDeleted removes the Service's members from its edge, or deletes its dedicated load balancer.
// Running it again after success changes nothing.
func (balancers *LoadBalancers) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	if edgeID := strings.TrimSpace(service.Annotations[AnnotationEdge]); edgeID != "" {
		if edgeError := balancers.removeEdgeMembers(ctx, clusterName, service, edgeID); edgeError != nil {
			return edgeError
		}
	}
	return balancers.deleteDedicated(ctx, clusterName, service)
}

// edgeStatus is the edge's load_balancer addresses: the IPv6 address of the VM running the role, then the
// edge's IPv4.
func edgeStatus(edge cloudapi.Edge) *v1.LoadBalancerStatus {
	publicIPv6 := ""
	publicIPv4 := edge.PublicIPv4
	for _, machine := range edge.Machines {
		if !slices.Contains(machine.Roles, edgeRoleLoadBalancer) {
			continue
		}
		if publicIPv6 == "" {
			publicIPv6 = machine.PublicIPv6
		}
		if publicIPv4 == "" {
			publicIPv4 = machine.PublicIPv4
		}
	}
	return ingressStatus(publicIPv6, publicIPv4)
}

func sameEdgeMembers(current []cloudapi.EdgeMember, desired []cloudapi.EdgeMember) bool {
	key := func(member cloudapi.EdgeMember) string {
		return fmt.Sprintf("%s|%d|%s|%d|%d|%t", member.Name, member.FrontendPort, member.Address, member.Port, member.Weight, member.Enabled)
	}
	currentKeys := make([]string, 0, len(current))
	for _, member := range current {
		currentKeys = append(currentKeys, key(member))
	}
	desiredKeys := make([]string, 0, len(desired))
	for _, member := range desired {
		desiredKeys = append(desiredKeys, key(member))
	}
	slices.Sort(currentKeys)
	slices.Sort(desiredKeys)
	return slices.Equal(currentKeys, desiredKeys)
}

// ensureEdge puts the Service's members on a combined edge's load_balancer role, keeping every other member of
// the edge as it is. A frontend port another member already uses is refused rather than shared.
func (balancers *LoadBalancers) ensureEdge(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node, settings serviceSettings, ports []servicePort) (*v1.LoadBalancerStatus, error) {
	edge, edgeError := balancers.api.GetEdge(ctx, settings.edgeID)
	if edgeError != nil {
		return nil, fmt.Errorf("get edge %s: %w", settings.edgeID, edgeError)
	}
	if edge.Placement != edgePlacementCombined || !slices.Contains(edge.Roles, edgeRoleLoadBalancer) {
		return nil, fmt.Errorf("edge %s is not a combined edge with the load_balancer role; drop the %s annotation to use a dedicated load balancer", edge.ID, AnnotationEdge)
	}
	prefix := edgeMemberPrefix(clusterName, service)
	var kept []cloudapi.EdgeMember
	portsInUse := map[int64]string{}
	for _, member := range edge.Members {
		if strings.HasPrefix(member.Name, prefix) {
			continue
		}
		kept = append(kept, member)
		portsInUse[member.FrontendPort] = member.Name
	}
	desired := slices.Clone(kept)
	for _, port := range ports {
		if owner, isUsed := portsInUse[port.port]; isUsed {
			return nil, fmt.Errorf("port %d of edge %s is already served by member %s", port.port, edge.ID, owner)
		}
		for _, member := range desiredMembers(service, nodes, port, true) {
			desired = append(desired, cloudapi.EdgeMember{
				Name: edgeMemberName(prefix, port.port, member.Address, member.Port), FrontendPort: port.port,
				Address: member.Address, Port: member.Port, Weight: defaultEdgeMemberWeight, Enabled: true,
			})
		}
	}
	if !sameEdgeMembers(edge.Members, desired) {
		if replaceError := balancers.api.ReplaceEdgeMembers(ctx, edge.ID, desired); replaceError != nil {
			return nil, fmt.Errorf("update the members of edge %s: %w", edge.ID, replaceError)
		}
		klog.InfoS("Updated edge members", "service", klog.KObj(service), "edge", edge.ID, "members", len(desired)-len(kept))
	}
	return edgeStatus(edge), nil
}

func (balancers *LoadBalancers) removeEdgeMembers(ctx context.Context, clusterName string, service *v1.Service, edgeID string) error {
	edge, edgeError := balancers.api.GetEdge(ctx, edgeID)
	if errors.Is(edgeError, cloudapi.ErrNotFound) {
		return nil
	}
	if edgeError != nil {
		return fmt.Errorf("get edge %s: %w", edgeID, edgeError)
	}
	prefix := edgeMemberPrefix(clusterName, service)
	kept := slices.DeleteFunc(slices.Clone(edge.Members), func(member cloudapi.EdgeMember) bool { return strings.HasPrefix(member.Name, prefix) })
	if len(kept) == len(edge.Members) {
		return nil
	}
	replaceError := balancers.api.ReplaceEdgeMembers(ctx, edge.ID, kept)
	if replaceError != nil && !errors.Is(replaceError, cloudapi.ErrNotFound) {
		return fmt.Errorf("remove the members of service %s from edge %s: %w", serviceKey(service), edge.ID, replaceError)
	}
	return nil
}
