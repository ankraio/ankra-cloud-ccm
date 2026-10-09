package provider

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	restclient "k8s.io/client-go/rest"
	cloudprovider "k8s.io/cloud-provider"
	cloudproviderapi "k8s.io/cloud-provider/api"
	nodecontroller "k8s.io/cloud-provider/controllers/node"
	servicecontroller "k8s.io/cloud-provider/controllers/service"
	"k8s.io/component-base/featuregate"
	controllersmetrics "k8s.io/component-base/metrics/prometheus/controllers"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

type fakeClientBuilder struct {
	client clientset.Interface
}

func (builder fakeClientBuilder) Config(string) (*restclient.Config, error) {
	return &restclient.Config{}, nil
}

func (builder fakeClientBuilder) ConfigOrDie(string) *restclient.Config {
	return &restclient.Config{}
}

func (builder fakeClientBuilder) Client(string) (clientset.Interface, error) {
	return builder.client, nil
}

func (builder fakeClientBuilder) ClientOrDie(string) clientset.Interface {
	return builder.client
}

// TestControllersInitialiseNodesAndServeServices runs the cloud-provider's own node and service controllers against
// a fake clientset and the fake API: an uninitialised node gets its providerID, addresses and topology labels and
// loses the uninitialized taint; a Service of type LoadBalancer gets an Ankra load balancer and its IPv6 ingress;
// the single-node event reaches the API server.
func TestControllersInitialiseNodesAndServeServices(t *testing.T) {
	api := newFakeAPI()
	api.servers["server-1"] = testServer()
	api.interfaces["server-1"] = []cloudapi.ServerInterface{{NetworkID: "network-1", Address: "10.0.0.5", Address6: "fd12::5"}}
	api.capabilities["de-fsn1"] = cloudapi.ZoneCapabilities{IsKnown: true, Stage: 1, LoadBalancerHA: false}

	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1"},
		Spec: v1.NodeSpec{Taints: []v1.Taint{{
			Key: cloudproviderapi.TaintExternalCloudProvider, Value: "true", Effect: v1.TaintEffectNoSchedule,
		}}},
		Status: v1.NodeStatus{Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}}},
	}
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-1"},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer, IPFamilies: []v1.IPFamily{v1.IPv6Protocol},
			Ports: []v1.ServicePort{{Name: "http", Port: 80, NodePort: 30080, Protocol: v1.ProtocolTCP}},
		},
	}
	client := fake.NewClientset(node, service)
	cloud := NewCloud(api, Options{HighAvailability: HighAvailabilityAuto})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cloud.Initialize(fakeClientBuilder{client: client}, ctx.Done())

	factory := informers.NewSharedInformerFactory(client, 0)
	metrics := controllersmetrics.NewControllerManagerMetrics("ankra-cloud-ccm-test")
	nodes, nodeError := nodecontroller.NewCloudNodeController(factory.Core().V1().Nodes(), client, cloud, time.Minute, 1, 1)
	if nodeError != nil {
		t.Fatal(nodeError)
	}
	services, serviceError := servicecontroller.New(cloud, client, factory.Core().V1().Services(), factory.Core().V1().Nodes(), testCluster, featuregate.NewFeatureGate())
	if serviceError != nil {
		t.Fatal(serviceError)
	}
	factory.Start(ctx.Done())
	go nodes.RunWithContext(ctx, metrics)
	go services.Run(ctx, 1, metrics)

	var initialised *v1.Node
	pollError := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		current, getError := client.CoreV1().Nodes().Get(ctx, "worker-1", metav1.GetOptions{})
		if getError != nil {
			return false, nil
		}
		initialised = current
		// The controller writes the addresses in a status update of its own after initialising the node, so the
		// poll waits for them too; reading the node between the two updates is what made this test flaky.
		return current.Spec.ProviderID != "" && len(current.Spec.Taints) == 0 && len(current.Status.Addresses) > 0, nil
	})
	if pollError != nil {
		t.Fatalf("node not initialised: %+v", initialised)
	}
	if initialised.Spec.ProviderID != "ankracloud://de-fsn1/server-1" {
		t.Fatalf("providerID %q", initialised.Spec.ProviderID)
	}
	expectedLabels := map[string]string{
		v1.LabelTopologyZone: "de-fsn1", v1.LabelTopologyRegion: "eu-central", v1.LabelInstanceTypeStable: "standard-2c-4g",
	}
	for key, value := range expectedLabels {
		if initialised.Labels[key] != value {
			t.Errorf("label %s = %q, want %q", key, initialised.Labels[key], value)
		}
	}
	if len(initialised.Status.Addresses) == 0 || initialised.Status.Addresses[0].Address != "fd64:1:0:2a::1" {
		t.Errorf("addresses %+v", initialised.Status.Addresses)
	}

	pollError = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		current, getError := client.CoreV1().Services("shop").Get(ctx, "web", metav1.GetOptions{})
		if getError != nil {
			return false, nil
		}
		return len(current.Status.LoadBalancer.Ingress) == 1 && current.Status.LoadBalancer.Ingress[0].IP == "fd64:1:0:9::1", nil
	})
	if pollError != nil {
		t.Fatalf("service ingress never set; creates %+v", api.creates)
	}
	if len(api.creates) != 1 || api.creates[0].HighAvailability {
		t.Fatalf("creates %+v", api.creates)
	}
	pollError = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		events, listError := client.CoreV1().Events("shop").List(ctx, metav1.ListOptions{})
		if listError != nil {
			return false, nil
		}
		for _, event := range events.Items {
			if event.Reason == EventSingleNodeLoadBalancer && event.InvolvedObject.Name == "web" {
				return true, nil
			}
		}
		return false, nil
	})
	if pollError != nil {
		t.Fatal("the single-node event never reached the API server")
	}
}

func TestTheProviderImplementsOnlyWhatItSupports(t *testing.T) {
	var cloud cloudprovider.Interface = NewCloud(newFakeAPI(), Options{HighAvailability: HighAvailabilityAuto})
	if _, isSupported := cloud.Routes(); isSupported {
		t.Error("routes are the CNI's")
	}
	if _, isSupported := cloud.Instances(); isSupported {
		t.Error("Instances is superseded by InstancesV2")
	}
	if _, isSupported := cloud.Zones(); isSupported {
		t.Error("zones come from InstancesV2")
	}
	if _, isSupported := cloud.Clusters(); isSupported {
		t.Error("clusters are not listed")
	}
	if _, isSupported := cloud.InstancesV2(); !isSupported {
		t.Error("InstancesV2 is required")
	}
	if _, isSupported := cloud.LoadBalancer(); !isSupported {
		t.Error("LoadBalancer is required")
	}
	if cloud.ProviderName() != "ankracloud" || !cloud.HasClusterID() {
		t.Error("provider name or cluster ID")
	}
}
