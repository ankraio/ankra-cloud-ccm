package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"

	"github.com/ankraio/ankra-cloud-ccm/internal/cloudapi"
)

// Service annotations the load balancer reads.
const (
	annotationPrefix = "load-balancer.ankra.cloud/"
	// AnnotationIPv4 "true" gives the load balancer an IPv4 frontend address: the priced IPv4 add-on, so off by
	// default. IPv6 is always on. Chosen when the load balancer is created.
	AnnotationIPv4 = annotationPrefix + "ipv4"
	// AnnotationNetworkID places the load balancer on this private network (default: ANKRA_CLOUD_NETWORK_ID).
	// Chosen when the load balancer is created.
	AnnotationNetworkID = annotationPrefix + "network-id"
	// AnnotationEdge serves the Service from an existing combined edge's load_balancer role instead of a
	// dedicated load balancer.
	AnnotationEdge = annotationPrefix + "edge"
	// AnnotationZone creates the load balancer in this zone (default: ANKRA_CLOUD_ZONE, then the nodes' zone).
	AnnotationZone = annotationPrefix + "zone"
	// AnnotationHealthCheckType is tcp (default), http or none.
	AnnotationHealthCheckType = annotationPrefix + "health-check-type"
	// AnnotationHealthCheckPath is the http check's path (default /).
	AnnotationHealthCheckPath = annotationPrefix + "health-check-path"
	// AnnotationHealthCheckExpectedStatus is the http check's expected status, 200 or a range such as 200-399.
	AnnotationHealthCheckExpectedStatus = annotationPrefix + "health-check-expected-status"
	// AnnotationHealthCheckInterval is the seconds between checks, 1-300 (default 2).
	AnnotationHealthCheckInterval = annotationPrefix + "health-check-interval"
	// AnnotationHealthCheckRise is the successes that bring a member back, 1-10 (default 2).
	AnnotationHealthCheckRise = annotationPrefix + "health-check-rise"
	// AnnotationHealthCheckFall is the failures that take a member out, 1-10 (default 3).
	AnnotationHealthCheckFall = annotationPrefix + "health-check-fall"
)

// Labels every load balancer the controller creates carries; the service label identifies it.
const (
	LabelService = "ccm.ankra.cloud/service"
	LabelCluster = "ccm.ankra.cloud/cluster"
)

// Health check defaults, the API's own (a TCP check every 2 s, rise 2, fall 3, path /, expected 200-399).
var defaultHealthCheck = cloudapi.HealthCheck{Type: "tcp", Path: "/", ExpectedStatus: "200-399", IntervalSeconds: 2, Rise: 2, Fall: 3}

var (
	healthCheckPathPattern   = regexp.MustCompile(`^/[A-Za-z0-9._~/?&=%-]{0,254}$`)
	expectedStatusPattern    = regexp.MustCompile(`^[1-5][0-9]{2}(-[1-5][0-9]{2})?$`)
	componentNameUnsafeChars = regexp.MustCompile(`[^a-z0-9-]+`)
)

// serviceSettings is what a Service's annotations ask for, with the cluster defaults filled in.
type serviceSettings struct {
	wantsIPv4   bool
	networkID   string
	edgeID      string
	zone        string
	healthCheck cloudapi.HealthCheck
}

func parseBoundedInteger(annotations map[string]string, key string, minimum int64, maximum int64) (int64, bool, error) {
	value, isSet := annotations[key]
	if !isSet || strings.TrimSpace(value) == "" {
		return 0, false, nil
	}
	parsed, parseError := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if parseError != nil || parsed < minimum || parsed > maximum {
		return 0, false, fmt.Errorf("annotation %s must be an integer from %d to %d, not %q", key, minimum, maximum, value)
	}
	return parsed, true, nil
}

// parseServiceSettings reads the Service's annotations over the provider's options.
func parseServiceSettings(service *v1.Service, options Options) (serviceSettings, error) {
	annotations := service.Annotations
	settings := serviceSettings{
		networkID:   options.DefaultNetworkID,
		zone:        options.DefaultZone,
		healthCheck: defaultHealthCheck,
	}
	if value, isSet := annotations[AnnotationIPv4]; isSet && strings.TrimSpace(value) != "" {
		wantsIPv4, parseError := strconv.ParseBool(strings.TrimSpace(value))
		if parseError != nil {
			return serviceSettings{}, fmt.Errorf("annotation %s must be true or false, not %q", AnnotationIPv4, value)
		}
		settings.wantsIPv4 = wantsIPv4
	}
	if value := strings.TrimSpace(annotations[AnnotationNetworkID]); value != "" {
		settings.networkID = value
	}
	if value := strings.TrimSpace(annotations[AnnotationZone]); value != "" {
		settings.zone = value
	}
	settings.edgeID = strings.TrimSpace(annotations[AnnotationEdge])
	if value := strings.TrimSpace(annotations[AnnotationHealthCheckType]); value != "" {
		switch value {
		case "tcp", "http", "none":
			settings.healthCheck.Type = value
		default:
			return serviceSettings{}, fmt.Errorf("annotation %s must be tcp, http or none, not %q", AnnotationHealthCheckType, value)
		}
	}
	if value := strings.TrimSpace(annotations[AnnotationHealthCheckPath]); value != "" {
		if !healthCheckPathPattern.MatchString(value) {
			return serviceSettings{}, fmt.Errorf("annotation %s must be a path starting with /, not %q", AnnotationHealthCheckPath, value)
		}
		settings.healthCheck.Path = value
	}
	if value := strings.TrimSpace(annotations[AnnotationHealthCheckExpectedStatus]); value != "" {
		if !expectedStatusPattern.MatchString(value) {
			return serviceSettings{}, fmt.Errorf("annotation %s must be a status such as 200 or a range such as 200-399, not %q", AnnotationHealthCheckExpectedStatus, value)
		}
		settings.healthCheck.ExpectedStatus = value
	}
	integerAnnotations := []struct {
		key     string
		minimum int64
		maximum int64
		target  *int64
	}{
		{AnnotationHealthCheckInterval, 1, 300, &settings.healthCheck.IntervalSeconds},
		{AnnotationHealthCheckRise, 1, 10, &settings.healthCheck.Rise},
		{AnnotationHealthCheckFall, 1, 10, &settings.healthCheck.Fall},
	}
	for _, annotation := range integerAnnotations {
		value, isSet, parseError := parseBoundedInteger(annotations, annotation.key, annotation.minimum, annotation.maximum)
		if parseError != nil {
			return serviceSettings{}, parseError
		}
		if isSet {
			*annotation.target = value
		}
	}
	return settings, nil
}

// serviceKey is the value of the service label: <namespace>/<name>.
func serviceKey(service *v1.Service) string {
	return service.Namespace + "/" + service.Name
}

func shortHash(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])[:8]
}

// loadBalancerName is the dedicated load balancer's name: k8s-<cluster>-<namespace>-<name>, shortened with a hash
// to the API's 64 characters.
func loadBalancerName(clusterName string, service *v1.Service) string {
	name := fmt.Sprintf("k8s-%s-%s-%s", clusterName, service.Namespace, service.Name)
	if len(name) <= 64 {
		return name
	}
	return name[:55] + "-" + shortHash(clusterName, service.Namespace, service.Name)
}

// componentName makes a frontend, backend or member name: lowercase letters, digits and dashes, at most 32.
func componentName(prefix string, suffix string) string {
	cleaned := strings.Trim(componentNameUnsafeChars.ReplaceAllString(strings.ToLower(prefix), "-"), "-")
	maximumPrefix := 32 - len(suffix) - 1
	if len(cleaned) > maximumPrefix {
		cleaned = strings.TrimRight(cleaned[:maximumPrefix], "-")
	}
	if cleaned == "" {
		return suffix
	}
	return cleaned + "-" + suffix
}

// listenerName names a port's frontend and backend: tcp-<port>.
func listenerName(port int32) string {
	return fmt.Sprintf("tcp-%d", port)
}

// memberName names a node's member of a backend: the node name and a hash of the node and address.
func memberName(nodeName string, address string) string {
	return componentName(nodeName, shortHash(nodeName, address))
}

// edgeMemberPrefix is what every edge member of the Service starts with, so members of other Services and those
// made by hand are never touched: k<hash of cluster, namespace and name>-.
func edgeMemberPrefix(clusterName string, service *v1.Service) string {
	return "k" + shortHash(clusterName, service.Namespace, service.Name) + "-"
}

func edgeMemberName(prefix string, frontendPort int64, address string, port int64) string {
	return prefix + shortHash(strconv.FormatInt(frontendPort, 10), address, strconv.FormatInt(port, 10))
}
