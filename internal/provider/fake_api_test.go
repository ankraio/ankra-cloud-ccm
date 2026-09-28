package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

// fakeAPI is an in-memory Ankra Cloud API that records every write.
type fakeAPI struct {
	lock sync.Mutex

	servers      map[string]cloudapi.Server
	interfaces   map[string][]cloudapi.ServerInterface
	zones        []cloudapi.Zone
	capabilities map[string]cloudapi.ZoneCapabilities

	loadBalancers map[string]*cloudapi.LoadBalancer
	edges         map[string]*cloudapi.Edge

	creates        []cloudapi.CreateLoadBalancerInput
	updates        []cloudapi.UpdateLoadBalancerInput
	memberReplaces int
	edgeReplaces   int
	deletes        []string
	calls          []string
	zoneListings   int
	sequence       int
	failCreate     error
	createAddress6 string
	createAddress4 string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		servers:        map[string]cloudapi.Server{},
		interfaces:     map[string][]cloudapi.ServerInterface{},
		zones:          []cloudapi.Zone{{Name: "de-fsn1", Region: "eu-central"}, {Name: "de-fsn2", Region: "eu-central"}},
		capabilities:   map[string]cloudapi.ZoneCapabilities{},
		loadBalancers:  map[string]*cloudapi.LoadBalancer{},
		edges:          map[string]*cloudapi.Edge{},
		createAddress6: "fd64:1:0:9::1",
	}
}

func (api *fakeAPI) nextID(prefix string) string {
	api.sequence++
	return fmt.Sprintf("%s-%d", prefix, api.sequence)
}

func (api *fakeAPI) record(call string) {
	api.calls = append(api.calls, call)
}

func (api *fakeAPI) GetServer(_ context.Context, serverID string) (cloudapi.Server, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	server, isKnown := api.servers[serverID]
	if !isKnown {
		return cloudapi.Server{}, fmt.Errorf("%w: server %s", cloudapi.ErrNotFound, serverID)
	}
	return server, nil
}

func (api *fakeAPI) FindServersByHostname(_ context.Context, hostname string) ([]cloudapi.Server, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	var found []cloudapi.Server
	for _, server := range api.servers {
		if server.Hostname == hostname {
			found = append(found, server)
		}
	}
	return found, nil
}

func (api *fakeAPI) ListServerInterfaces(_ context.Context, serverID string) ([]cloudapi.ServerInterface, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	return api.interfaces[serverID], nil
}

func (api *fakeAPI) ListZones(context.Context) ([]cloudapi.Zone, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.zoneListings++
	return api.zones, nil
}

func (api *fakeAPI) GetZoneCapabilities(_ context.Context, zone string) (cloudapi.ZoneCapabilities, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	return api.capabilities[zone], nil
}

func summary(balancer cloudapi.LoadBalancer) cloudapi.LoadBalancer {
	balancer.Frontends = nil
	balancer.Backends = nil
	balancer.Labels = maps.Clone(balancer.Labels)
	return balancer
}

func (api *fakeAPI) ListLoadBalancers(context.Context) ([]cloudapi.LoadBalancer, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	var all []cloudapi.LoadBalancer
	for _, balancer := range api.loadBalancers {
		all = append(all, summary(*balancer))
	}
	slices.SortFunc(all, func(left cloudapi.LoadBalancer, right cloudapi.LoadBalancer) int {
		if left.ID < right.ID {
			return -1
		}
		return 1
	})
	return all, nil
}

func (api *fakeAPI) balancer(loadBalancerID string) (*cloudapi.LoadBalancer, error) {
	balancer, isKnown := api.loadBalancers[loadBalancerID]
	if !isKnown {
		return nil, fmt.Errorf("%w: load balancer %s", cloudapi.ErrNotFound, loadBalancerID)
	}
	return balancer, nil
}

func (api *fakeAPI) GetLoadBalancer(_ context.Context, loadBalancerID string) (cloudapi.LoadBalancer, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return cloudapi.LoadBalancer{}, lookupError
	}
	copied := *balancer
	copied.Frontends = slices.Clone(balancer.Frontends)
	copied.Backends = nil
	for _, backend := range balancer.Backends {
		backend.Members = slices.Clone(backend.Members)
		copied.Backends = append(copied.Backends, backend)
	}
	return copied, nil
}

func (api *fakeAPI) CreateLoadBalancer(_ context.Context, input cloudapi.CreateLoadBalancerInput) (cloudapi.LoadBalancer, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("create_load_balancer")
	if api.failCreate != nil {
		return cloudapi.LoadBalancer{}, api.failCreate
	}
	api.creates = append(api.creates, input)
	isHighlyAvailable := input.HighAvailability
	balancer := &cloudapi.LoadBalancer{
		ID: api.nextID("lb"), Name: input.Name, Zone: input.Zone, NetworkID: input.NetworkID, State: "running",
		Labels: maps.Clone(input.Labels), PublicIPv6: api.createAddress6, HighAvailability: &isHighlyAvailable,
	}
	if input.PublicIPv4 {
		balancer.PublicIPv4 = api.createAddress4
	}
	api.loadBalancers[balancer.ID] = balancer
	return summary(*balancer), nil
}

func (api *fakeAPI) UpdateLoadBalancer(_ context.Context, loadBalancerID string, input cloudapi.UpdateLoadBalancerInput) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("update_load_balancer")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return lookupError
	}
	api.updates = append(api.updates, input)
	if input.Name != nil {
		balancer.Name = *input.Name
	}
	if input.Labels != nil {
		balancer.Labels = maps.Clone(input.Labels)
	}
	return nil
}

func (api *fakeAPI) DeleteLoadBalancer(_ context.Context, loadBalancerID string) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("delete_load_balancer")
	if _, lookupError := api.balancer(loadBalancerID); lookupError != nil {
		return lookupError
	}
	api.deletes = append(api.deletes, loadBalancerID)
	delete(api.loadBalancers, loadBalancerID)
	return nil
}

func (api *fakeAPI) CreateBackend(_ context.Context, loadBalancerID string, input cloudapi.BackendInput) (cloudapi.Backend, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("create_load_balancer_backend")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return cloudapi.Backend{}, lookupError
	}
	backend := cloudapi.Backend{ID: api.nextID("backend"), Name: input.Name, Mode: input.Mode, HealthCheck: input.HealthCheck}
	balancer.Backends = append(balancer.Backends, backend)
	return backend, nil
}

func (api *fakeAPI) UpdateBackend(_ context.Context, loadBalancerID string, backendID string, input cloudapi.BackendInput) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("update_load_balancer_backend")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return lookupError
	}
	for index := range balancer.Backends {
		if balancer.Backends[index].ID == backendID {
			balancer.Backends[index].HealthCheck = input.HealthCheck
			return nil
		}
	}
	return fmt.Errorf("%w: backend %s", cloudapi.ErrNotFound, backendID)
}

func (api *fakeAPI) DeleteBackend(_ context.Context, loadBalancerID string, backendID string) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("delete_load_balancer_backend")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return lookupError
	}
	balancer.Backends = slices.DeleteFunc(balancer.Backends, func(backend cloudapi.Backend) bool { return backend.ID == backendID })
	return nil
}

func (api *fakeAPI) CreateFrontend(_ context.Context, loadBalancerID string, input cloudapi.FrontendInput) (cloudapi.Frontend, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("create_load_balancer_frontend")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return cloudapi.Frontend{}, lookupError
	}
	for _, frontend := range balancer.Frontends {
		if frontend.Port == input.Port {
			return cloudapi.Frontend{}, fmt.Errorf("port %d is taken", input.Port)
		}
	}
	frontend := cloudapi.Frontend{ID: api.nextID("frontend"), Name: input.Name, Port: input.Port, BackendID: input.BackendID}
	balancer.Frontends = append(balancer.Frontends, frontend)
	return frontend, nil
}

func (api *fakeAPI) DeleteFrontend(_ context.Context, loadBalancerID string, frontendID string) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("delete_load_balancer_frontend")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return lookupError
	}
	balancer.Frontends = slices.DeleteFunc(balancer.Frontends, func(frontend cloudapi.Frontend) bool { return frontend.ID == frontendID })
	return nil
}

func (api *fakeAPI) ReplaceMembers(_ context.Context, loadBalancerID string, backendID string, members []cloudapi.Member) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("replace_load_balancer_members")
	balancer, lookupError := api.balancer(loadBalancerID)
	if lookupError != nil {
		return lookupError
	}
	api.memberReplaces++
	for index := range balancer.Backends {
		if balancer.Backends[index].ID == backendID {
			balancer.Backends[index].Members = slices.Clone(members)
			return nil
		}
	}
	return fmt.Errorf("%w: backend %s", cloudapi.ErrNotFound, backendID)
}

func (api *fakeAPI) GetEdge(_ context.Context, edgeID string) (cloudapi.Edge, error) {
	api.lock.Lock()
	defer api.lock.Unlock()
	edge, isKnown := api.edges[edgeID]
	if !isKnown {
		return cloudapi.Edge{}, fmt.Errorf("%w: edge %s", cloudapi.ErrNotFound, edgeID)
	}
	copied := *edge
	copied.Members = slices.Clone(edge.Members)
	return copied, nil
}

func (api *fakeAPI) ReplaceEdgeMembers(_ context.Context, edgeID string, members []cloudapi.EdgeMember) error {
	api.lock.Lock()
	defer api.lock.Unlock()
	api.record("update_edge")
	edge, isKnown := api.edges[edgeID]
	if !isKnown {
		return fmt.Errorf("%w: edge %s", cloudapi.ErrNotFound, edgeID)
	}
	api.edgeReplaces++
	edge.Members = slices.Clone(members)
	return nil
}

func (api *fakeAPI) countCalls(call string) int {
	api.lock.Lock()
	defer api.lock.Unlock()
	count := 0
	for _, recorded := range api.calls {
		if recorded == call {
			count++
		}
	}
	return count
}
