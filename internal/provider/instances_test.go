package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cloudprovider "k8s.io/cloud-provider"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

func TestProviderIDRoundTrips(t *testing.T) {
	identifier := ProviderID{Zone: "de-fsn1", ServerID: "0f9c2d1e-7b"}
	formatted := identifier.String()
	if formatted != "ankracloud://de-fsn1/0f9c2d1e-7b" {
		t.Fatalf("formatted %q", formatted)
	}
	parsed, parseError := ParseProviderID(formatted)
	if parseError != nil || parsed != identifier {
		t.Fatalf("parsed %+v, %v", parsed, parseError)
	}
}

func TestParseProviderIDRejectsOtherShapes(t *testing.T) {
	for _, value := range []string{
		"", "hcloud://123", "ankracloud://", "ankracloud://de-fsn1", "ankracloud://de-fsn1/", "ankracloud:///server",
		"ankracloud://de-fsn1/server/extra", "ankracloud:/de-fsn1/server",
	} {
		if _, parseError := ParseProviderID(value); parseError == nil {
			t.Errorf("%q parsed", value)
		}
	}
}

func testServer() cloudapi.Server {
	return cloudapi.Server{
		ID: "server-1", Zone: "de-fsn1", Hostname: "worker-1", Plan: "standard-2c-4g", State: "started",
		PublicIPv6: "fd64:1:0:2a::1", PublicIPv6Prefix: "fd64:1:0:2a::/64",
	}
}

func nodeNamed(name string, providerID string) *v1.Node {
	return &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1.NodeSpec{ProviderID: providerID}}
}

func TestNodeAddressesAreIPv6First(t *testing.T) {
	server := testServer()
	server.PublicIPv4 = "203.0.113.7"
	interfaces := []cloudapi.ServerInterface{
		{NetworkID: "network-1", Address: "10.0.0.5", Address6: "fd12:3456:789a::5"},
		{NetworkID: "network-2", Address: "10.1.0.5", Address6: "fd12:3456:789b::5"},
	}
	expected := []v1.NodeAddress{
		{Type: v1.NodeExternalIP, Address: "fd64:1:0:2a::1"},
		{Type: v1.NodeExternalIP, Address: "203.0.113.7"},
		{Type: v1.NodeInternalIP, Address: "fd12:3456:789a::5"},
		{Type: v1.NodeInternalIP, Address: "fd12:3456:789b::5"},
		{Type: v1.NodeInternalIP, Address: "10.0.0.5"},
		{Type: v1.NodeInternalIP, Address: "10.1.0.5"},
		{Type: v1.NodeHostName, Address: "worker-1"},
	}
	if addresses := NodeAddresses(server, interfaces); !reflect.DeepEqual(addresses, expected) {
		t.Fatalf("addresses %+v", addresses)
	}
}

func TestNodeAddressesOfAnIPv6OnlyServerWithoutAddOn(t *testing.T) {
	expected := []v1.NodeAddress{
		{Type: v1.NodeExternalIP, Address: "fd64:1:0:2a::1"},
		{Type: v1.NodeHostName, Address: "worker-1"},
	}
	if addresses := NodeAddresses(testServer(), nil); !reflect.DeepEqual(addresses, expected) {
		t.Fatalf("addresses %+v", addresses)
	}
}

func TestInstanceMetadataByProviderID(t *testing.T) {
	api := newFakeAPI()
	api.servers["server-1"] = testServer()
	api.interfaces["server-1"] = []cloudapi.ServerInterface{{NetworkID: "network-1", Address: "10.0.0.5", Address6: "fd12::5"}}
	instances := NewInstances(api)
	metadata, metadataError := instances.InstanceMetadata(context.Background(), nodeNamed("anything", "ankracloud://de-fsn1/server-1"))
	if metadataError != nil {
		t.Fatal(metadataError)
	}
	if metadata.ProviderID != "ankracloud://de-fsn1/server-1" || metadata.InstanceType != "standard-2c-4g" ||
		metadata.Zone != "de-fsn1" || metadata.Region != "eu-central" {
		t.Fatalf("metadata %+v", metadata)
	}
	if len(metadata.NodeAddresses) != 4 || metadata.NodeAddresses[0].Address != "fd64:1:0:2a::1" {
		t.Fatalf("addresses %+v", metadata.NodeAddresses)
	}
	if _, secondError := instances.InstanceMetadata(context.Background(), nodeNamed("anything", "ankracloud://de-fsn1/server-1")); secondError != nil {
		t.Fatal(secondError)
	}
	if api.zoneListings != 1 {
		t.Fatalf("zones listed %d times, want once (cached)", api.zoneListings)
	}
}

func TestInstanceMetadataByHostnameWithoutProviderID(t *testing.T) {
	api := newFakeAPI()
	api.servers["server-1"] = testServer()
	instances := NewInstances(api)
	metadata, metadataError := instances.InstanceMetadata(context.Background(), nodeNamed("worker-1", ""))
	if metadataError != nil {
		t.Fatal(metadataError)
	}
	if metadata.ProviderID != "ankracloud://de-fsn1/server-1" {
		t.Fatalf("providerID %q", metadata.ProviderID)
	}
	qualified, qualifiedError := instances.InstanceMetadata(context.Background(), nodeNamed("worker-1.cluster.example", ""))
	if qualifiedError != nil || qualified.ProviderID != metadata.ProviderID {
		t.Fatalf("fully qualified node name: %+v, %v", qualified, qualifiedError)
	}
}

func TestInstanceMetadataRefusesAnAmbiguousHostname(t *testing.T) {
	api := newFakeAPI()
	api.servers["server-1"] = testServer()
	twin := testServer()
	twin.ID = "server-2"
	api.servers["server-2"] = twin
	if _, metadataError := NewInstances(api).InstanceMetadata(context.Background(), nodeNamed("worker-1", "")); metadataError == nil {
		t.Fatal("two servers with one hostname resolved")
	}
}

func TestInstanceNotFound(t *testing.T) {
	api := newFakeAPI()
	instances := NewInstances(api)
	for _, node := range []*v1.Node{nodeNamed("worker-9", "ankracloud://de-fsn1/gone"), nodeNamed("worker-9", "")} {
		exists, existsError := instances.InstanceExists(context.Background(), node)
		if existsError != nil || exists {
			t.Fatalf("exists %t, %v", exists, existsError)
		}
		shutdown, shutdownError := instances.InstanceShutdown(context.Background(), node)
		if shutdownError != nil || shutdown {
			t.Fatalf("shutdown %t, %v", shutdown, shutdownError)
		}
		if _, metadataError := instances.InstanceMetadata(context.Background(), node); !errors.Is(metadataError, cloudprovider.InstanceNotFound) {
			t.Fatalf("metadata error %v", metadataError)
		}
	}
}

func TestInstanceExistsRejectsAMalformedProviderID(t *testing.T) {
	if _, existsError := NewInstances(newFakeAPI()).InstanceExists(context.Background(), nodeNamed("worker-1", "aws:///i-123")); existsError == nil {
		t.Fatal("a foreign providerID was accepted")
	}
}

func TestInstanceShutdownFollowsTheServerState(t *testing.T) {
	cases := map[string]struct {
		isShutdown bool
		exists     bool
	}{
		"started":  {isShutdown: false, exists: true},
		"starting": {isShutdown: false, exists: true},
		"stopping": {isShutdown: true, exists: true},
		"stopped":  {isShutdown: true, exists: true},
		"error":    {isShutdown: false, exists: true},
		"deleting": {isShutdown: false, exists: false},
	}
	for state, expected := range cases {
		api := newFakeAPI()
		server := testServer()
		server.State = state
		api.servers["server-1"] = server
		instances := NewInstances(api)
		node := nodeNamed("worker-1", "ankracloud://de-fsn1/server-1")
		shutdown, shutdownError := instances.InstanceShutdown(context.Background(), node)
		if shutdownError != nil || shutdown != expected.isShutdown {
			t.Errorf("%s: shutdown %t, %v", state, shutdown, shutdownError)
		}
		exists, existsError := instances.InstanceExists(context.Background(), node)
		if existsError != nil || exists != expected.exists {
			t.Errorf("%s: exists %t, %v", state, exists, existsError)
		}
	}
}
