// Package cloudapi is the slice of the Ankra Cloud public API the cloud controller manager uses: an interface the
// provider depends on, the types it reads and writes, and an implementation on the generated OpenAPI client.
package cloudapi

import (
	"context"
	"errors"
)

// ErrNotFound is returned when the API answers 404 for the resource asked for.
var ErrNotFound = errors.New("not found")

// Server states the controller distinguishes.
const (
	ServerStateStopping = "stopping"
	ServerStateStopped  = "stopped"
	ServerStateDeleting = "deleting"
)

// Server is a customer server as the controller needs it.
type Server struct {
	ID               string
	Zone             string
	Hostname         string
	Plan             string
	State            string
	PublicIPv6       string
	PublicIPv6Prefix string
	PublicIPv4       string
	Labels           map[string]string
}

// ServerInterface is one of a server's private network legs: its RFC1918 IPv4 address and its ULA IPv6 address.
type ServerInterface struct {
	NetworkID string
	Address   string
	Address6  string
}

// Zone is an Ankra Cloud zone and the region it belongs to.
type Zone struct {
	Name   string
	Region string
}

// ZoneCapabilities is what a zone's growth stage allows. IsKnown is false when the API cannot say (an older API
// without GET /v1/zones/{zone}/capabilities), in which case the controller assumes a highly available zone. Stage is
// 1 for a zone of one server, 2 for two and 3 from three on (0 before any server registered); ComputeNodeCount is the
// zone's compute nodes, or its servers on an API that does not report compute nodes.
type ZoneCapabilities struct {
	IsKnown          bool
	Stage            int
	LoadBalancerHA   bool
	ComputeNodeCount int
}

// HealthCheck is a backend's health check; empty fields keep the API's defaults.
type HealthCheck struct {
	Type            string
	Path            string
	ExpectedStatus  string
	IntervalSeconds int64
	Rise            int64
	Fall            int64
}

// Member is one backend member: a node address and its NodePort.
type Member struct {
	Name    string
	Address string
	Port    int64
}

// Backend is a load balancer backend with its members.
type Backend struct {
	ID          string
	Name        string
	Mode        string
	HealthCheck HealthCheck
	Members     []Member
}

// Frontend is a load balancer frontend: a port that forwards to a backend.
type Frontend struct {
	ID        string
	Name      string
	Port      int64
	BackendID string
}

// LoadBalancer is a dedicated Ankra load balancer. PublicIPv6 and PublicIPv4 hold its addresses by family, whether
// the API reports them as `public_ipv6`/`public_ipv4` or as the older single `address`.
type LoadBalancer struct {
	ID               string
	Name             string
	Zone             string
	NetworkID        string
	State            string
	Labels           map[string]string
	PublicIPv6       string
	PublicIPv4       string
	HighAvailability *bool
	Frontends        []Frontend
	Backends         []Backend
}

// CreateLoadBalancerInput creates a load balancer. HighAvailability is sent only when false (a single VM on a zone
// with one compute node) and PublicIPv4 only when true (the priced IPv4 add-on), so an API that predates either
// field is never sent it for the default.
type CreateLoadBalancerInput struct {
	Name             string
	Zone             string
	NetworkID        string
	Labels           map[string]string
	HighAvailability bool
	PublicIPv4       bool
}

// UpdateLoadBalancerInput is PATCH /v1/load-balancers/{id} (update_load_balancer): nil fields keep their value.
type UpdateLoadBalancerInput struct {
	Name   *string
	Labels map[string]string
}

// BackendInput creates or updates a backend.
type BackendInput struct {
	Name        string
	Mode        string
	HealthCheck HealthCheck
}

// FrontendInput creates a frontend.
type FrontendInput struct {
	Name      string
	Port      int64
	BackendID string
}

// EdgeMember is a member of a combined edge's load_balancer role: the edge listens on FrontendPort (IPv4 and IPv6)
// and forwards to Address:Port.
type EdgeMember struct {
	Name         string
	FrontendPort int64
	Address      string
	Port         int64
	Weight       int64
	Enabled      bool
}

// EdgeMachine is one VM of an edge with its public addresses and roles.
type EdgeMachine struct {
	Roles      []string
	PublicIPv6 string
	PublicIPv4 string
}

// Edge is a network edge.
type Edge struct {
	ID         string
	Zone       string
	NetworkID  string
	Placement  string
	State      string
	Roles      []string
	PublicIPv4 string
	Machines   []EdgeMachine
	Members    []EdgeMember
}

// API is every call the cloud controller manager makes.
type API interface {
	GetServer(ctx context.Context, serverID string) (Server, error)
	// FindServersByHostname is list_servers?hostname=, an exact match.
	FindServersByHostname(ctx context.Context, hostname string) ([]Server, error)
	ListServerInterfaces(ctx context.Context, serverID string) ([]ServerInterface, error)
	ListZones(ctx context.Context) ([]Zone, error)
	GetZoneCapabilities(ctx context.Context, zone string) (ZoneCapabilities, error)

	ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error)
	GetLoadBalancer(ctx context.Context, loadBalancerID string) (LoadBalancer, error)
	CreateLoadBalancer(ctx context.Context, input CreateLoadBalancerInput) (LoadBalancer, error)
	// UpdateLoadBalancer is update_load_balancer (PATCH name and labels).
	UpdateLoadBalancer(ctx context.Context, loadBalancerID string, input UpdateLoadBalancerInput) error
	DeleteLoadBalancer(ctx context.Context, loadBalancerID string) error
	CreateBackend(ctx context.Context, loadBalancerID string, input BackendInput) (Backend, error)
	UpdateBackend(ctx context.Context, loadBalancerID string, backendID string, input BackendInput) error
	DeleteBackend(ctx context.Context, loadBalancerID string, backendID string) error
	CreateFrontend(ctx context.Context, loadBalancerID string, input FrontendInput) (Frontend, error)
	DeleteFrontend(ctx context.Context, loadBalancerID string, frontendID string) error
	// ReplaceMembers is replace_load_balancer_members (PUT the backend's whole member list).
	ReplaceMembers(ctx context.Context, loadBalancerID string, backendID string, members []Member) error

	GetEdge(ctx context.Context, edgeID string) (Edge, error)
	// ReplaceEdgeMembers is update_edge with only `members`: the edge's whole member list.
	ReplaceEdgeMembers(ctx context.Context, edgeID string, members []EdgeMember) error
}
