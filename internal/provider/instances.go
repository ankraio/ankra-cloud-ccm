package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

// Instances implements cloudprovider.InstancesV2 on Ankra servers.
type Instances struct {
	api cloudapi.API

	regionsLock sync.Mutex
	regions     map[string]string
}

// NewInstances builds the node implementation.
func NewInstances(api cloudapi.API) *Instances {
	return &Instances{api: api, regions: map[string]string{}}
}

// serverOf finds the node's server: by providerID when the node has one, otherwise by its hostname (the node name,
// then its first label for a node registered under a fully qualified name). It answers
// cloudprovider.InstanceNotFound when there is no such server.
func (instances *Instances) serverOf(ctx context.Context, node *v1.Node) (cloudapi.Server, error) {
	if node.Spec.ProviderID != "" {
		identifier, parseError := ParseProviderID(node.Spec.ProviderID)
		if parseError != nil {
			return cloudapi.Server{}, parseError
		}
		server, getError := instances.api.GetServer(ctx, identifier.ServerID)
		if errors.Is(getError, cloudapi.ErrNotFound) {
			return cloudapi.Server{}, cloudprovider.InstanceNotFound
		}
		if getError != nil {
			return cloudapi.Server{}, fmt.Errorf("get server %s: %w", identifier.ServerID, getError)
		}
		return server, nil
	}
	candidates := []string{node.Name}
	if shortName, _, isQualified := strings.Cut(node.Name, "."); isQualified && shortName != "" {
		candidates = append(candidates, shortName)
	}
	for _, hostname := range candidates {
		servers, findError := instances.api.FindServersByHostname(ctx, hostname)
		if findError != nil {
			return cloudapi.Server{}, fmt.Errorf("find server by hostname %s: %w", hostname, findError)
		}
		switch len(servers) {
		case 0:
			continue
		case 1:
			return servers[0], nil
		default:
			return cloudapi.Server{}, fmt.Errorf("%d servers have the hostname %s; set the node's providerID to %s<zone>/<server-id>", len(servers), hostname, providerIDPrefix)
		}
	}
	return cloudapi.Server{}, cloudprovider.InstanceNotFound
}

// InstanceExists is false once the server is gone or being deleted.
func (instances *Instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	server, serverError := instances.serverOf(ctx, node)
	if errors.Is(serverError, cloudprovider.InstanceNotFound) {
		return false, nil
	}
	if serverError != nil {
		return false, serverError
	}
	return server.State != cloudapi.ServerStateDeleting, nil
}

// InstanceShutdown is true while the server is stopping or stopped.
func (instances *Instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	server, serverError := instances.serverOf(ctx, node)
	if errors.Is(serverError, cloudprovider.InstanceNotFound) {
		return false, nil
	}
	if serverError != nil {
		return false, serverError
	}
	return server.State == cloudapi.ServerStateStopping || server.State == cloudapi.ServerStateStopped, nil
}

// InstanceMetadata reports the node's providerID, plan, addresses, zone and region.
func (instances *Instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	server, serverError := instances.serverOf(ctx, node)
	if serverError != nil {
		return nil, serverError
	}
	interfaces, interfacesError := instances.api.ListServerInterfaces(ctx, server.ID)
	if interfacesError != nil {
		return nil, fmt.Errorf("list the interfaces of server %s: %w", server.ID, interfacesError)
	}
	region, regionError := instances.regionOf(ctx, server.Zone)
	if regionError != nil {
		return nil, regionError
	}
	return &cloudprovider.InstanceMetadata{
		ProviderID:    ProviderID{Zone: server.Zone, ServerID: server.ID}.String(),
		InstanceType:  server.Plan,
		NodeAddresses: NodeAddresses(server, interfaces),
		Zone:          server.Zone,
		Region:        region,
	}, nil
}

// regionOf answers the zone's region from list_zones, cached for the life of the process (zones do not move).
func (instances *Instances) regionOf(ctx context.Context, zone string) (string, error) {
	instances.regionsLock.Lock()
	defer instances.regionsLock.Unlock()
	if region, isCached := instances.regions[zone]; isCached {
		return region, nil
	}
	zones, listError := instances.api.ListZones(ctx)
	if listError != nil {
		return "", fmt.Errorf("list zones: %w", listError)
	}
	for _, candidate := range zones {
		instances.regions[candidate.Name] = candidate.Region
	}
	region, isKnown := instances.regions[zone]
	if !isKnown {
		klog.InfoS("Zone is not listed by the API; the node gets no region label", "zone", zone)
	}
	return region, nil
}

// NodeAddresses orders a server's addresses IPv6 first: the public IPv6 address, the public IPv4 add-on when the
// server holds it, the private legs' ULA IPv6 addresses, their RFC1918 IPv4 addresses, then the hostname.
func NodeAddresses(server cloudapi.Server, interfaces []cloudapi.ServerInterface) []v1.NodeAddress {
	var addresses []v1.NodeAddress
	seen := map[string]bool{}
	add := func(addressType v1.NodeAddressType, value string, wantsIPv6 bool) {
		parsed, parseError := netip.ParseAddr(strings.TrimSpace(value))
		if parseError != nil || isIPv6(parsed) != wantsIPv6 {
			return
		}
		text := parsed.String()
		if seen[text] {
			return
		}
		seen[text] = true
		addresses = append(addresses, v1.NodeAddress{Type: addressType, Address: text})
	}
	add(v1.NodeExternalIP, server.PublicIPv6, true)
	add(v1.NodeExternalIP, server.PublicIPv4, false)
	for _, networkInterface := range interfaces {
		add(v1.NodeInternalIP, networkInterface.Address6, true)
	}
	for _, networkInterface := range interfaces {
		add(v1.NodeInternalIP, networkInterface.Address, false)
	}
	if server.Hostname != "" {
		addresses = append(addresses, v1.NodeAddress{Type: v1.NodeHostName, Address: server.Hostname})
	}
	return addresses
}

func isIPv6(address netip.Addr) bool {
	return address.Is6() && !address.Is4In6()
}
