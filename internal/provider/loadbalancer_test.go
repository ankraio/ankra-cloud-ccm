package provider

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

const testCluster = "production"

var componentNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func loadBalancerService(annotations map[string]string, families []v1.IPFamily, ports ...v1.ServicePort) *v1.Service {
	if len(ports) == 0 {
		ports = []v1.ServicePort{{Name: "http", Port: 80, NodePort: 30080, Protocol: v1.ProtocolTCP}}
	}
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-1", Annotations: annotations},
		Spec:       v1.ServiceSpec{Type: v1.ServiceTypeLoadBalancer, Ports: ports, IPFamilies: families},
	}
}

func clusterNode(name string, externalIPv6 string, internalIPv6 string, internalIPv4 string) *v1.Node {
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{zoneLabel: "de-fsn1"}}}
	if externalIPv6 != "" {
		node.Status.Addresses = append(node.Status.Addresses, v1.NodeAddress{Type: v1.NodeExternalIP, Address: externalIPv6})
	}
	if internalIPv6 != "" {
		node.Status.Addresses = append(node.Status.Addresses, v1.NodeAddress{Type: v1.NodeInternalIP, Address: internalIPv6})
	}
	if internalIPv4 != "" {
		node.Status.Addresses = append(node.Status.Addresses, v1.NodeAddress{Type: v1.NodeInternalIP, Address: internalIPv4})
	}
	node.Status.Addresses = append(node.Status.Addresses, v1.NodeAddress{Type: v1.NodeHostName, Address: name})
	return node
}

func twoNodes() []*v1.Node {
	return []*v1.Node{
		clusterNode("worker-2", "fd64:1:0:2b::1", "fd12::6", "10.0.0.6"),
		clusterNode("worker-1", "fd64:1:0:2a::1", "fd12::5", "10.0.0.5"),
	}
}

func newTestLoadBalancers(api *fakeAPI, options Options) (*LoadBalancers, *record.FakeRecorder) {
	if options.HighAvailability == "" {
		options.HighAvailability = HighAvailabilityAuto
	}
	balancers := NewLoadBalancers(api, options)
	recorder := record.NewFakeRecorder(32)
	balancers.SetRecorder(recorder)
	return balancers, recorder
}

func onlyLoadBalancer(t *testing.T, api *fakeAPI) *cloudapi.LoadBalancer {
	t.Helper()
	if len(api.loadBalancers) != 1 {
		t.Fatalf("%d load balancers, want 1", len(api.loadBalancers))
	}
	for _, balancer := range api.loadBalancers {
		return balancer
	}
	return nil
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestEnsureCreatesOneLabelledLoadBalancerPerService(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(nil, []v1.IPFamily{v1.IPv6Protocol},
		v1.ServicePort{Name: "http", Port: 80, NodePort: 30080},
		v1.ServicePort{Name: "https", Port: 443, NodePort: 30443, Protocol: v1.ProtocolTCP})
	status, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes())
	if ensureError != nil {
		t.Fatal(ensureError)
	}
	if !reflect.DeepEqual(status.Ingress, []v1.LoadBalancerIngress{{IP: "fd64:1:0:9::1"}}) {
		t.Fatalf("ingress %+v", status.Ingress)
	}
	balancer := onlyLoadBalancer(t, api)
	if balancer.Name != "k8s-production-shop-web" || balancer.Zone != "de-fsn1" {
		t.Fatalf("load balancer %+v", balancer)
	}
	if balancer.Labels[LabelService] != "shop/web" || balancer.Labels[LabelCluster] != testCluster {
		t.Fatalf("labels %v", balancer.Labels)
	}
	if created := api.creates[0]; created.PublicIPv4 || !created.HighAvailability {
		t.Fatalf("create %+v: want IPv6 only and highly available", created)
	}
	if len(balancer.Frontends) != 2 || len(balancer.Backends) != 2 {
		t.Fatalf("frontends %+v backends %+v", balancer.Frontends, balancer.Backends)
	}
	for _, backend := range balancer.Backends {
		if backend.HealthCheck != defaultHealthCheck || backend.Mode != "tcp" {
			t.Fatalf("backend %+v", backend)
		}
		if len(backend.Members) != 2 || backend.Members[0].Address != "fd64:1:0:2a::1" || backend.Members[1].Address != "fd64:1:0:2b::1" {
			t.Fatalf("members of %s: %+v (want the nodes' public IPv6 without a network)", backend.Name, backend.Members)
		}
	}
	for _, frontend := range balancer.Frontends {
		if frontend.Name != listenerName(int32(frontend.Port)) {
			t.Fatalf("frontend %+v", frontend)
		}
	}

	callsBefore := len(api.calls)
	if _, againError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); againError != nil {
		t.Fatal(againError)
	}
	if len(api.calls) != callsBefore {
		t.Fatalf("a second ensure wrote %v", api.calls[callsBefore:])
	}
}

func TestEnsureAdoptsAnUnlabelledLoadBalancerByName(t *testing.T) {
	api := newFakeAPI()
	api.loadBalancers["lb-orphan"] = &cloudapi.LoadBalancer{ID: "lb-orphan", Name: "k8s-production-shop-web", Zone: "de-fsn1", State: "running", PublicIPv6: "fd64:1:0:8::1"}
	balancers, _ := newTestLoadBalancers(api, Options{})
	status, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes())
	if ensureError != nil {
		t.Fatal(ensureError)
	}
	if len(api.creates) != 0 || status.Ingress[0].IP != "fd64:1:0:8::1" {
		t.Fatalf("creates %+v status %+v", api.creates, status)
	}
	if api.loadBalancers["lb-orphan"].Labels[LabelService] != "shop/web" {
		t.Fatalf("labels %v", api.loadBalancers["lb-orphan"].Labels)
	}
}

func TestEnsureDoesNotAdoptAnotherServicesLoadBalancer(t *testing.T) {
	api := newFakeAPI()
	api.loadBalancers["lb-other"] = &cloudapi.LoadBalancer{
		ID: "lb-other", Name: "k8s-production-shop-web", State: "running", PublicIPv6: "fd64:1:0:8::1",
		Labels: map[string]string{LabelService: "shop/api", LabelCluster: testCluster},
	}
	balancers, _ := newTestLoadBalancers(api, Options{})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if len(api.creates) != 1 {
		t.Fatalf("creates %+v", api.creates)
	}
}

func TestUpdateReplacesMembersWhenNodesChange(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(nil, []v1.IPFamily{v1.IPv6Protocol})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	replacesBefore := api.memberReplaces
	if updateError := balancers.UpdateLoadBalancer(context.Background(), testCluster, service, twoNodes()); updateError != nil {
		t.Fatal(updateError)
	}
	if api.memberReplaces != replacesBefore {
		t.Fatal("unchanged nodes replaced the members")
	}
	grown := append(twoNodes(), clusterNode("worker-3", "fd64:1:0:2c::1", "", ""))
	if updateError := balancers.UpdateLoadBalancer(context.Background(), testCluster, service, grown); updateError != nil {
		t.Fatal(updateError)
	}
	members := onlyLoadBalancer(t, api).Backends[0].Members
	if api.memberReplaces != replacesBefore+1 || len(members) != 3 || members[2].Address != "fd64:1:0:2c::1" || members[2].Port != 30080 {
		t.Fatalf("replaces %d members %+v", api.memberReplaces-replacesBefore, members)
	}
	if updateError := balancers.UpdateLoadBalancer(context.Background(), testCluster, service, grown[:1]); updateError != nil {
		t.Fatal(updateError)
	}
	if members := onlyLoadBalancer(t, api).Backends[0].Members; len(members) != 1 || members[0].Address != "fd64:1:0:2b::1" {
		t.Fatalf("members after a node left: %+v", members)
	}
}

func TestUpdateWithoutALoadBalancerFails(t *testing.T) {
	balancers, _ := newTestLoadBalancers(newFakeAPI(), Options{})
	if updateError := balancers.UpdateLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); updateError == nil {
		t.Fatal("updated a load balancer that does not exist")
	}
}

func TestDualStackMembersAreIPv6FirstAndPrivateOnANetwork(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{DefaultNetworkID: "network-1"})
	service := loadBalancerService(nil, []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	var addresses []string
	for _, member := range onlyLoadBalancer(t, api).Backends[0].Members {
		addresses = append(addresses, member.Address)
	}
	expected := []string{"fd12::5", "fd12::6", "10.0.0.5", "10.0.0.6"}
	if !reflect.DeepEqual(addresses, expected) {
		t.Fatalf("members %v, want %v", addresses, expected)
	}
	if api.creates[0].NetworkID != "network-1" {
		t.Fatalf("network %q", api.creates[0].NetworkID)
	}
}

func TestIPv4IsOptIn(t *testing.T) {
	api := newFakeAPI()
	api.createAddress4 = "203.0.113.50"
	balancers, _ := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(map[string]string{AnnotationIPv4: "true"}, nil)
	status, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes())
	if ensureError != nil {
		t.Fatal(ensureError)
	}
	if !api.creates[0].PublicIPv4 {
		t.Fatal("the IPv4 annotation did not ask for IPv4")
	}
	expected := []v1.LoadBalancerIngress{{IP: "fd64:1:0:9::1"}, {IP: "203.0.113.50"}}
	if !reflect.DeepEqual(status.Ingress, expected) {
		t.Fatalf("ingress %+v", status.Ingress)
	}
}

func TestIPv4AskedForAfterCreationIsReported(t *testing.T) {
	api := newFakeAPI()
	balancers, recorder := newTestLoadBalancers(api, Options{})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(map[string]string{AnnotationIPv4: "true"}, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	events := drainEvents(recorder)
	if len(api.creates) != 1 || len(events) != 1 || !strings.Contains(events[0], EventImmutableSetting) {
		t.Fatalf("creates %d events %v", len(api.creates), events)
	}
}

func TestAnnotationsAreValidated(t *testing.T) {
	for key, value := range map[string]string{
		AnnotationIPv4:                      "maybe",
		AnnotationHealthCheckType:           "udp",
		AnnotationHealthCheckPath:           "healthz",
		AnnotationHealthCheckExpectedStatus: "ok",
		AnnotationHealthCheckInterval:       "0",
		AnnotationHealthCheckRise:           "11",
		AnnotationHealthCheckFall:           "x",
	} {
		api := newFakeAPI()
		balancers, _ := newTestLoadBalancers(api, Options{})
		if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(map[string]string{key: value}, nil), twoNodes()); ensureError == nil {
			t.Errorf("%s=%q accepted", key, value)
		}
		if len(api.creates) != 0 {
			t.Errorf("%s=%q created a load balancer", key, value)
		}
	}
}

func TestHealthCheckAnnotationsConfigureAndUpdateTheBackends(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	annotations := map[string]string{
		AnnotationHealthCheckType: "http", AnnotationHealthCheckPath: "/healthz", AnnotationHealthCheckExpectedStatus: "200",
		AnnotationHealthCheckInterval: "5", AnnotationHealthCheckRise: "3", AnnotationHealthCheckFall: "4",
	}
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(annotations, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	expected := cloudapi.HealthCheck{Type: "http", Path: "/healthz", ExpectedStatus: "200", IntervalSeconds: 5, Rise: 3, Fall: 4}
	if check := onlyLoadBalancer(t, api).Backends[0].HealthCheck; check != expected {
		t.Fatalf("health check %+v", check)
	}
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if check := onlyLoadBalancer(t, api).Backends[0].HealthCheck; check != defaultHealthCheck || api.countCalls("update_load_balancer_backend") != 1 {
		t.Fatalf("health check %+v after the annotations went", check)
	}
}

func TestNetworkAndZoneAnnotationsWinOverTheDefaults(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{DefaultNetworkID: "network-default", DefaultZone: "de-fsn1"})
	service := loadBalancerService(map[string]string{AnnotationNetworkID: "network-2", AnnotationZone: "de-fsn2"}, nil)
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if created := api.creates[0]; created.NetworkID != "network-2" || created.Zone != "de-fsn2" {
		t.Fatalf("create %+v", created)
	}
}

func TestChangedPortsReplaceTheirListeners(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	changed := loadBalancerService(nil, nil, v1.ServicePort{Name: "http", Port: 8080, NodePort: 30081})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, changed, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	balancer := onlyLoadBalancer(t, api)
	if len(balancer.Frontends) != 1 || balancer.Frontends[0].Port != 8080 || len(balancer.Backends) != 1 || balancer.Backends[0].Name != "tcp-8080" {
		t.Fatalf("frontends %+v backends %+v", balancer.Frontends, balancer.Backends)
	}
	if balancer.Backends[0].Members[0].Port != 30081 {
		t.Fatalf("members %+v", balancer.Backends[0].Members)
	}
}

func TestNonTCPPortsAreSkippedWithAnEvent(t *testing.T) {
	api := newFakeAPI()
	balancers, recorder := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(nil, nil,
		v1.ServicePort{Name: "dns", Port: 53, NodePort: 30053, Protocol: v1.ProtocolUDP},
		v1.ServicePort{Name: "dns-tcp", Port: 53, NodePort: 30054, Protocol: v1.ProtocolTCP})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.Contains(events[0], EventUnsupportedPort) || !strings.Contains(events[0], "53/UDP") {
		t.Fatalf("events %v", events)
	}
	if len(onlyLoadBalancer(t, api).Frontends) != 1 {
		t.Fatal("the UDP port got a frontend")
	}
}

func TestAPortWithoutANodePortIsRefused(t *testing.T) {
	balancers, _ := newTestLoadBalancers(newFakeAPI(), Options{})
	service := loadBalancerService(nil, nil, v1.ServicePort{Name: "http", Port: 80})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError == nil {
		t.Fatal("a port without a NodePort was accepted")
	}
}

func TestASingleComputeNodeZoneGetsASingleVMLoadBalancer(t *testing.T) {
	api := newFakeAPI()
	api.capabilities["de-fsn1"] = cloudapi.ZoneCapabilities{IsKnown: true, Stage: 1, LoadBalancerHA: false, ComputeNodeCount: 1}
	balancers, recorder := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(nil, nil)
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if api.creates[0].HighAvailability {
		t.Fatal("a single-node zone asked for a load balancer pair")
	}
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.Contains(events[0], "Normal "+EventSingleNodeLoadBalancer) || !strings.Contains(events[0], "high_availability: false") {
		t.Fatalf("events %v", events)
	}
	if _, againError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); againError != nil {
		t.Fatal(againError)
	}
	if again := drainEvents(recorder); len(again) != 0 {
		t.Fatalf("the single-node event repeated: %v", again)
	}
}

func TestHighAvailabilityModes(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		capabilities cloudapi.ZoneCapabilities
		expected     bool
	}{
		{name: "auto on an API without capabilities", mode: HighAvailabilityAuto, expected: true},
		{name: "auto on a grown zone", mode: HighAvailabilityAuto, capabilities: cloudapi.ZoneCapabilities{IsKnown: true, LoadBalancerHA: true}, expected: true},
		{name: "never", mode: HighAvailabilityNever, capabilities: cloudapi.ZoneCapabilities{IsKnown: true, LoadBalancerHA: true}, expected: false},
		{name: "always", mode: HighAvailabilityAlways, capabilities: cloudapi.ZoneCapabilities{IsKnown: true}, expected: true},
	}
	for _, testCase := range cases {
		api := newFakeAPI()
		api.capabilities["de-fsn1"] = testCase.capabilities
		balancers, _ := newTestLoadBalancers(api, Options{HighAvailability: testCase.mode})
		if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError != nil {
			t.Fatalf("%s: %v", testCase.name, ensureError)
		}
		if api.creates[0].HighAvailability != testCase.expected {
			t.Errorf("%s: high availability %t", testCase.name, api.creates[0].HighAvailability)
		}
	}
}

func TestAZonelessLoadBalancerIsRefused(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	node := clusterNode("worker-1", "fd64:1:0:2a::1", "", "")
	node.Labels = nil
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), []*v1.Node{node}); ensureError == nil {
		t.Fatal("created a load balancer without a zone")
	}
	node.Spec.ProviderID = "ankracloud://de-fsn2/server-1"
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), []*v1.Node{node}); ensureError != nil {
		t.Fatal(ensureError)
	}
	if api.creates[0].Zone != "de-fsn2" {
		t.Fatalf("zone %q, want the providerID's", api.creates[0].Zone)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	api := newFakeAPI()
	balancers, _ := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(nil, nil)
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes()); ensureError != nil {
		t.Fatal(ensureError)
	}
	if _, exists, getError := balancers.GetLoadBalancer(context.Background(), testCluster, service); getError != nil || !exists {
		t.Fatalf("exists %t, %v", exists, getError)
	}
	for range 2 {
		if deleteError := balancers.EnsureLoadBalancerDeleted(context.Background(), testCluster, service); deleteError != nil {
			t.Fatal(deleteError)
		}
	}
	if len(api.deletes) != 1 || len(api.loadBalancers) != 0 {
		t.Fatalf("deletes %v", api.deletes)
	}
	if _, exists, getError := balancers.GetLoadBalancer(context.Background(), testCluster, service); getError != nil || exists {
		t.Fatalf("exists %t, %v after delete", exists, getError)
	}
}

type notFoundOnDelete struct {
	*fakeAPI
}

func (api notFoundOnDelete) DeleteLoadBalancer(context.Context, string) error {
	return cloudapi.ErrNotFound
}

func TestDeleteToleratesALoadBalancerAlreadyGone(t *testing.T) {
	api := newFakeAPI()
	api.loadBalancers["lb-1"] = &cloudapi.LoadBalancer{ID: "lb-1", State: "running", Labels: map[string]string{LabelService: "shop/web", LabelCluster: testCluster}}
	balancers := NewLoadBalancers(notFoundOnDelete{api}, Options{HighAvailability: HighAvailabilityAuto})
	if deleteError := balancers.EnsureLoadBalancerDeleted(context.Background(), testCluster, loadBalancerService(nil, nil)); deleteError != nil {
		t.Fatal(deleteError)
	}
}

func combinedEdge() *cloudapi.Edge {
	return &cloudapi.Edge{
		ID: "edge-1", Zone: "de-fsn1", NetworkID: "network-1", Placement: "combined", State: "running",
		Roles: []string{"bastion", "load_balancer"}, PublicIPv4: "203.0.113.9",
		Machines: []cloudapi.EdgeMachine{{Roles: []string{"bastion", "load_balancer"}, PublicIPv6: "fd64:1:0:77::1", PublicIPv4: "203.0.113.9"}},
		Members: []cloudapi.EdgeMember{
			{Name: "by-hand", FrontendPort: 8443, Address: "10.0.0.40", Port: 443, Weight: 100, Enabled: true},
		},
	}
}

func TestEdgeModeUsesTheCombinedEdgesLoadBalancerRole(t *testing.T) {
	api := newFakeAPI()
	api.edges["edge-1"] = combinedEdge()
	api.loadBalancers["lb-before"] = &cloudapi.LoadBalancer{ID: "lb-before", State: "running", Labels: map[string]string{LabelService: "shop/web", LabelCluster: testCluster}}
	balancers, _ := newTestLoadBalancers(api, Options{})
	service := loadBalancerService(map[string]string{AnnotationEdge: "edge-1"}, []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol})
	status, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, service, twoNodes())
	if ensureError != nil {
		t.Fatal(ensureError)
	}
	if !reflect.DeepEqual(status.Ingress, []v1.LoadBalancerIngress{{IP: "fd64:1:0:77::1"}, {IP: "203.0.113.9"}}) {
		t.Fatalf("ingress %+v", status.Ingress)
	}
	if len(api.creates) != 0 || len(api.deletes) != 1 || api.deletes[0] != "lb-before" {
		t.Fatalf("creates %+v deletes %v: edge mode must not create and must retire the dedicated one", api.creates, api.deletes)
	}
	members := api.edges["edge-1"].Members
	if len(members) != 5 || members[0].Name != "by-hand" {
		t.Fatalf("members %+v", members)
	}
	expectedAddresses := []string{"fd12::5", "fd12::6", "10.0.0.5", "10.0.0.6"}
	for index, member := range members[1:] {
		if member.FrontendPort != 80 || member.Port != 30080 || member.Address != expectedAddresses[index] || member.Weight != 100 || !member.Enabled {
			t.Fatalf("member %d %+v", index, member)
		}
	}
	replaces := api.edgeReplaces
	if updateError := balancers.UpdateLoadBalancer(context.Background(), testCluster, service, twoNodes()); updateError != nil {
		t.Fatal(updateError)
	}
	if api.edgeReplaces != replaces {
		t.Fatal("an unchanged edge was rewritten")
	}
	if _, exists, getError := balancers.GetLoadBalancer(context.Background(), testCluster, service); getError != nil || !exists {
		t.Fatalf("exists %t, %v", exists, getError)
	}
	for range 2 {
		if deleteError := balancers.EnsureLoadBalancerDeleted(context.Background(), testCluster, service); deleteError != nil {
			t.Fatal(deleteError)
		}
	}
	if members := api.edges["edge-1"].Members; len(members) != 1 || members[0].Name != "by-hand" || api.edgeReplaces != replaces+1 {
		t.Fatalf("members %+v after delete (replaces %d)", members, api.edgeReplaces-replaces)
	}
}

func TestEdgeModeRefusesATakenPortAndASeparateEdge(t *testing.T) {
	api := newFakeAPI()
	api.edges["edge-1"] = combinedEdge()
	balancers, _ := newTestLoadBalancers(api, Options{})
	taken := loadBalancerService(map[string]string{AnnotationEdge: "edge-1"}, nil, v1.ServicePort{Name: "https", Port: 8443, NodePort: 30443})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, taken, twoNodes()); ensureError == nil {
		t.Fatal("a port another member serves was taken")
	}
	separate := combinedEdge()
	separate.Placement = "separate"
	api.edges["edge-1"] = separate
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(map[string]string{AnnotationEdge: "edge-1"}, nil), twoNodes()); ensureError == nil {
		t.Fatal("a separate edge was used as a combined one")
	}
	if api.edgeReplaces != 0 {
		t.Fatalf("edge written %d times", api.edgeReplaces)
	}
}

func TestEdgeModeDeleteToleratesAMissingEdge(t *testing.T) {
	balancers, _ := newTestLoadBalancers(newFakeAPI(), Options{})
	service := loadBalancerService(map[string]string{AnnotationEdge: "edge-gone"}, nil)
	if deleteError := balancers.EnsureLoadBalancerDeleted(context.Background(), testCluster, service); deleteError != nil {
		t.Fatal(deleteError)
	}
	if _, exists, getError := balancers.GetLoadBalancer(context.Background(), testCluster, service); getError != nil || exists {
		t.Fatalf("exists %t, %v", exists, getError)
	}
}

func TestCreateFailureIsReturned(t *testing.T) {
	api := newFakeAPI()
	api.failCreate = errors.New("quota exceeded")
	balancers, _ := newTestLoadBalancers(api, Options{})
	if _, ensureError := balancers.EnsureLoadBalancer(context.Background(), testCluster, loadBalancerService(nil, nil), twoNodes()); ensureError == nil || !strings.Contains(ensureError.Error(), "quota exceeded") {
		t.Fatalf("error %v", ensureError)
	}
}

func TestNamesFitTheAPI(t *testing.T) {
	service := loadBalancerService(nil, nil)
	service.Namespace = strings.Repeat("n", 63)
	service.Name = strings.Repeat("s", 63)
	if name := loadBalancerName(testCluster, service); len(name) != 64 {
		t.Fatalf("name %q has %d characters", name, len(name))
	}
	for _, nodeName := range []string{"worker-1", strings.Repeat("Very.Long.Node.Name", 5), "---"} {
		name := memberName(nodeName, "fd12::5")
		if len(name) > 32 || !componentNamePattern.MatchString(name) {
			t.Errorf("member name %q", name)
		}
	}
	edgeName := edgeMemberName(edgeMemberPrefix(testCluster, service), 443, "fd12::5", 30443)
	if len(edgeName) > 32 || !componentNamePattern.MatchString(edgeName) {
		t.Errorf("edge member name %q", edgeName)
	}
}

func TestOptionsFromEnvironment(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, isSet := values[key]
			return value, isSet
		}
	}
	options, optionsError := OptionsFromEnvironment(lookup(map[string]string{NetworkIDEnvironmentVariable: " network-1 ", HighAvailabilityEnvironmentVariable: "FALSE"}))
	if optionsError != nil || options.DefaultNetworkID != "network-1" || options.HighAvailability != HighAvailabilityNever {
		t.Fatalf("options %+v, %v", options, optionsError)
	}
	if defaults, defaultsError := OptionsFromEnvironment(lookup(nil)); defaultsError != nil || defaults.HighAvailability != HighAvailabilityAuto {
		t.Fatalf("defaults %+v, %v", defaults, defaultsError)
	}
	if _, invalidError := OptionsFromEnvironment(lookup(map[string]string{HighAvailabilityEnvironmentVariable: "sometimes"})); invalidError == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
