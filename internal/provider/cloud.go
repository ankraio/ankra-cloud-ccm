// Package provider is the Ankra Cloud implementation of k8s.io/cloud-provider: InstancesV2 maps nodes to Ankra
// servers and LoadBalancer turns Services of type LoadBalancer into Ankra load balancers (or members of a combined
// edge's load_balancer role). Routes are not implemented; the CNI routes pod traffic.
package provider

import (
	"fmt"
	"io"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

// ProviderName is the --cloud-provider value and the scheme of every providerID.
const ProviderName = "ankracloud"

// Environment variables with the provider's defaults (the chart sets them from its values).
const (
	NetworkIDEnvironmentVariable        = "ANKRA_CLOUD_NETWORK_ID"
	ZoneEnvironmentVariable             = "ANKRA_CLOUD_ZONE"
	HighAvailabilityEnvironmentVariable = "ANKRA_CLOUD_LOAD_BALANCER_HIGH_AVAILABILITY"
)

// High availability modes of new load balancers.
const (
	HighAvailabilityAuto   = "auto"
	HighAvailabilityAlways = "true"
	HighAvailabilityNever  = "false"
)

// Options are the provider's cluster-wide defaults.
type Options struct {
	// DefaultNetworkID is the private network a load balancer is placed on when the Service does not name one.
	DefaultNetworkID string
	// DefaultZone is the zone of a new load balancer when the Service does not name one; otherwise the nodes' zone.
	DefaultZone string
	// HighAvailability is auto (ask the zone's capabilities), true or false.
	HighAvailability string
}

// OptionsFromEnvironment reads Options through lookup.
func OptionsFromEnvironment(lookup func(string) (string, bool)) (Options, error) {
	options := Options{HighAvailability: HighAvailabilityAuto}
	if value, isSet := lookup(NetworkIDEnvironmentVariable); isSet {
		options.DefaultNetworkID = strings.TrimSpace(value)
	}
	if value, isSet := lookup(ZoneEnvironmentVariable); isSet {
		options.DefaultZone = strings.TrimSpace(value)
	}
	if value, isSet := lookup(HighAvailabilityEnvironmentVariable); isSet && strings.TrimSpace(value) != "" {
		options.HighAvailability = strings.ToLower(strings.TrimSpace(value))
	}
	switch options.HighAvailability {
	case HighAvailabilityAuto, HighAvailabilityAlways, HighAvailabilityNever:
	default:
		return Options{}, fmt.Errorf("%s must be auto, true or false, not %q", HighAvailabilityEnvironmentVariable, options.HighAvailability)
	}
	return options, nil
}

// Cloud is the cloudprovider.Interface of Ankra Cloud.
type Cloud struct {
	instances     *Instances
	loadBalancers *LoadBalancers
}

// NewCloud builds the provider on an API.
func NewCloud(api cloudapi.API, options Options) *Cloud {
	return &Cloud{
		instances:     NewInstances(api),
		loadBalancers: NewLoadBalancers(api, options),
	}
}

// Register registers the provider under ProviderName, reading its configuration from lookup (os.LookupEnv) when
// the controller manager starts it. The --cloud-config file is not used.
func Register(lookup func(string) (string, bool), userAgent string) {
	cloudprovider.RegisterCloudProvider(ProviderName, func(io.Reader) (cloudprovider.Interface, error) {
		configuration, configurationError := cloudapi.ConfigurationFromEnvironment(lookup)
		if configurationError != nil {
			return nil, configurationError
		}
		options, optionsError := OptionsFromEnvironment(lookup)
		if optionsError != nil {
			return nil, optionsError
		}
		httpClient, httpError := configuration.HTTPClient()
		if httpError != nil {
			return nil, httpError
		}
		client, clientError := cloudapi.NewClient(configuration.APIURL, configuration.Token, httpClient, userAgent)
		if clientError != nil {
			return nil, clientError
		}
		return NewCloud(client, options), nil
	})
}

// Initialize starts the event recorder the load balancer uses to explain itself on Services.
func (cloud *Cloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	client := clientBuilder.ClientOrDie("ankra-cloud-ccm")
	broadcaster := record.NewBroadcaster()
	broadcaster.StartStructuredLogging(0)
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: client.CoreV1().Events("")})
	cloud.loadBalancers.SetRecorder(broadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "ankra-cloud-ccm"}))
	go func() {
		<-stop
		broadcaster.Shutdown()
	}()
	klog.InfoS("Ankra Cloud provider initialised", "provider", ProviderName)
}

// LoadBalancer returns the Service load balancer implementation.
func (cloud *Cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	return cloud.loadBalancers, true
}

// Instances is not implemented; InstancesV2 is.
func (cloud *Cloud) Instances() (cloudprovider.Instances, bool) {
	return nil, false
}

// InstancesV2 returns the node implementation.
func (cloud *Cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return cloud.instances, true
}

// Zones is not implemented; InstancesV2 reports zone and region.
func (cloud *Cloud) Zones() (cloudprovider.Zones, bool) {
	return nil, false
}

// Clusters is not implemented.
func (cloud *Cloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

// Routes is not implemented: the CNI routes pod traffic between nodes.
func (cloud *Cloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

// ProviderName returns ankracloud.
func (cloud *Cloud) ProviderName() string {
	return ProviderName
}

// HasClusterID is true: load balancers carry the cluster name (--cluster-name) as a label.
func (cloud *Cloud) HasClusterID() bool {
	return true
}
