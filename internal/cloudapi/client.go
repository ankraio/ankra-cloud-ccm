package cloudapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/ankraio/ankra-cloud-ccm/internal/ankraapi"
)

// Client implements API on the generated OpenAPI client.
type Client struct {
	generated *ankraapi.Client
}

// NewClient builds a client for the API at endpoint with an API token. httpClient carries the TLS configuration
// (an optional private CA bundle) and timeouts.
func NewClient(endpoint string, token string, httpClient *http.Client, userAgent string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("an API token is required")
	}
	generated, clientError := ankraapi.New(endpoint, ankraapi.APITokenCredential(token), ankraapi.WithHTTPClient(httpClient), ankraapi.WithUserAgent(userAgent))
	if clientError != nil {
		return nil, clientError
	}
	return &Client{generated: generated}, nil
}

func translateError(callError error) error {
	var apiError *ankraapi.Error
	if errors.As(callError, &apiError) && apiError.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrNotFound, callError)
	}
	return callError
}

func statusCodeOf(callError error) int {
	var apiError *ankraapi.Error
	if errors.As(callError, &apiError) {
		return apiError.StatusCode
	}
	return 0
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func serverFromAPI(server ankraapi.Server) Server {
	return Server{
		ID: server.ID, Zone: server.Zone, Hostname: server.Hostname, Plan: server.Plan, State: server.State,
		PublicIPv6: stringValue(server.PublicIPv6), PublicIPv6Prefix: stringValue(server.PublicIPv6Prefix),
		PublicIPv4: stringValue(server.PublicIPv4), Labels: server.Labels,
	}
}

// GetServer calls get_server.
func (client *Client) GetServer(ctx context.Context, serverID string) (Server, error) {
	answer, callError := client.generated.GetServer(ctx, ankraapi.GetServerParameters{ID: serverID})
	if callError != nil {
		return Server{}, translateError(callError)
	}
	return serverFromAPI(answer.Server), nil
}

// FindServersByHostname calls list_servers with the exact-match `hostname` query parameter. The operation exists
// today; the parameter is added by the API lane, so it goes through Call, which passes any query.
func (client *Client) FindServersByHostname(ctx context.Context, hostname string) ([]Server, error) {
	query := url.Values{"hostname": []string{hostname}}
	answer, callError := client.generated.Call(ctx, "list_servers", nil, query, nil)
	if callError != nil {
		return nil, translateError(callError)
	}
	var list ankraapi.ServerList
	if decodeError := json.Unmarshal(answer.Body, &list); decodeError != nil {
		return nil, fmt.Errorf("list_servers: decode the answer: %w", decodeError)
	}
	servers := make([]Server, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Hostname == hostname {
			servers = append(servers, serverFromAPI(item))
		}
	}
	return servers, nil
}

// ListServerInterfaces calls list_server_interfaces.
func (client *Client) ListServerInterfaces(ctx context.Context, serverID string) ([]ServerInterface, error) {
	answer, callError := client.generated.ListServerInterfaces(ctx, ankraapi.ListServerInterfacesParameters{ID: serverID})
	if callError != nil {
		return nil, translateError(callError)
	}
	interfaces := make([]ServerInterface, 0, len(answer.Items))
	for _, item := range answer.Items {
		interfaces = append(interfaces, ServerInterface{NetworkID: item.NetworkID, Address: item.Address, Address6: item.Address6})
	}
	return interfaces, nil
}

// ListZones calls list_zones.
func (client *Client) ListZones(ctx context.Context) ([]Zone, error) {
	answer, callError := client.generated.ListZones(ctx)
	if callError != nil {
		return nil, translateError(callError)
	}
	zones := make([]Zone, 0, len(answer.Items))
	for _, item := range answer.Items {
		zones = append(zones, Zone{Name: item.Name, Region: item.Region})
	}
	return zones, nil
}

// zoneCapabilitiesDocument is the part of ZoneCapabilities (get_zone_capabilities) the controller reads. It decodes
// only these fields so that a change elsewhere in the document cannot stop load balancers from being created.
type zoneCapabilitiesDocument struct {
	Stage        int  `json:"stage"`
	Servers      int  `json:"servers"`
	ComputeNodes *int `json:"compute_nodes"`
	Features     struct {
		LoadBalancerHA bool `json:"load_balancer_ha"`
	} `json:"features"`
}

// GetZoneCapabilities calls get_zone_capabilities. An API without it (404, 405) or one that keeps it
// from API tokens (401, 403) answers IsKnown false rather than an error.
func (client *Client) GetZoneCapabilities(ctx context.Context, zone string) (ZoneCapabilities, error) {
	answer, callError := client.generated.Call(ctx, "get_zone_capabilities", map[string]string{"zone": zone}, nil, nil)
	if callError != nil {
		switch statusCodeOf(callError) {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized, http.StatusForbidden:
			return ZoneCapabilities{}, nil
		}
		return ZoneCapabilities{}, translateError(callError)
	}
	var document zoneCapabilitiesDocument
	if decodeError := json.Unmarshal(answer.Body, &document); decodeError != nil {
		return ZoneCapabilities{}, fmt.Errorf("get_zone_capabilities: decode the answer: %w", decodeError)
	}
	capabilities := ZoneCapabilities{
		IsKnown: true, Stage: document.Stage, LoadBalancerHA: document.Features.LoadBalancerHA, ComputeNodeCount: document.Servers,
	}
	if document.ComputeNodes != nil {
		capabilities.ComputeNodeCount = *document.ComputeNodes
	}
	return capabilities, nil
}

// loadBalancerDocument decodes a load balancer with the fields the IPv6-first model adds (`public_ipv6`,
// `public_ipv4`, `labels`, `high_availability`) next to today's single `address`.
type loadBalancerDocument struct {
	ID               string                          `json:"id"`
	Name             string                          `json:"name"`
	Zone             string                          `json:"zone"`
	NetworkID        string                          `json:"network_id"`
	State            string                          `json:"state"`
	Address          *string                         `json:"address"`
	PublicIPv6       *string                         `json:"public_ipv6"`
	PublicIPv4       *string                         `json:"public_ipv4"`
	Labels           map[string]string               `json:"labels"`
	HighAvailability *bool                           `json:"high_availability"`
	Frontends        []ankraapi.LoadBalancerFrontend `json:"frontends"`
	Backends         []ankraapi.LoadBalancerBackend  `json:"backends"`
}

func backendFromAPI(backend ankraapi.LoadBalancerBackend) Backend {
	converted := Backend{
		ID: backend.ID, Name: backend.Name, Mode: backend.Mode,
		HealthCheck: HealthCheck{
			Type: backend.HealthCheck.Type, Path: backend.HealthCheck.Path, ExpectedStatus: backend.HealthCheck.ExpectedStatus,
			IntervalSeconds: backend.HealthCheck.IntervalSeconds, Rise: backend.HealthCheck.Rise, Fall: backend.HealthCheck.Fall,
		},
	}
	for _, member := range backend.Members {
		converted.Members = append(converted.Members, Member{Name: member.Name, Address: member.Address, Port: member.Port})
	}
	return converted
}

func (document loadBalancerDocument) toLoadBalancer() LoadBalancer {
	balancer := LoadBalancer{
		ID: document.ID, Name: document.Name, Zone: document.Zone, NetworkID: document.NetworkID, State: document.State,
		Labels: document.Labels, PublicIPv6: stringValue(document.PublicIPv6), PublicIPv4: stringValue(document.PublicIPv4),
		HighAvailability: document.HighAvailability,
	}
	if address, parseError := netip.ParseAddr(stringValue(document.Address)); parseError == nil {
		if address.Is4() && balancer.PublicIPv4 == "" {
			balancer.PublicIPv4 = address.String()
		}
		if address.Is6() && !address.Is4In6() && balancer.PublicIPv6 == "" {
			balancer.PublicIPv6 = address.String()
		}
	}
	for _, frontend := range document.Frontends {
		balancer.Frontends = append(balancer.Frontends, Frontend{ID: frontend.ID, Name: frontend.Name, Port: frontend.Port, BackendID: frontend.BackendID})
	}
	for _, backend := range document.Backends {
		balancer.Backends = append(balancer.Backends, backendFromAPI(backend))
	}
	return balancer
}

// listLoadBalancersPageSize is the largest page list_load_balancers serves.
const listLoadBalancersPageSize = "100"

// ListLoadBalancers calls list_load_balancers (without frontends and backends) and follows `next_cursor` to the last
// page.
func (client *Client) ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	var balancers []LoadBalancer
	cursor := ""
	for {
		query := url.Values{"limit": []string{listLoadBalancersPageSize}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		answer, callError := client.generated.Call(ctx, "list_load_balancers", nil, query, nil)
		if callError != nil {
			return nil, translateError(callError)
		}
		var page struct {
			Items      []loadBalancerDocument `json:"items"`
			NextCursor *string                `json:"next_cursor"`
		}
		if decodeError := json.Unmarshal(answer.Body, &page); decodeError != nil {
			return nil, fmt.Errorf("list_load_balancers: decode the answer: %w", decodeError)
		}
		for _, item := range page.Items {
			balancers = append(balancers, item.toLoadBalancer())
		}
		if page.NextCursor == nil || *page.NextCursor == "" || *page.NextCursor == cursor {
			return balancers, nil
		}
		cursor = *page.NextCursor
	}
}

func decodeLoadBalancerEnvelope(operationID string, content []byte) (LoadBalancer, error) {
	var envelope struct {
		LoadBalancer loadBalancerDocument `json:"load_balancer"`
	}
	if decodeError := json.Unmarshal(content, &envelope); decodeError != nil {
		return LoadBalancer{}, fmt.Errorf("%s: decode the answer: %w", operationID, decodeError)
	}
	return envelope.LoadBalancer.toLoadBalancer(), nil
}

// GetLoadBalancer calls get_load_balancer.
func (client *Client) GetLoadBalancer(ctx context.Context, loadBalancerID string) (LoadBalancer, error) {
	answer, callError := client.generated.Call(ctx, "get_load_balancer", map[string]string{"id": loadBalancerID}, nil, nil)
	if callError != nil {
		return LoadBalancer{}, translateError(callError)
	}
	return decodeLoadBalancerEnvelope("get_load_balancer", answer.Body)
}

type createLoadBalancerBody struct {
	Zone             string            `json:"zone"`
	Name             string            `json:"name"`
	NetworkID        string            `json:"network_id"`
	Labels           map[string]string `json:"labels,omitempty"`
	HighAvailability *bool             `json:"high_availability,omitempty"`
	PublicIPv4       bool              `json:"public_ipv4"`
}

// CreateLoadBalancer calls create_load_balancer with the labels, and then update_load_balancer for any label the
// answer does not carry (an API whose create took none). public_ipv4 is always sent: the API adds the priced IPv4
// address when it is left out. high_availability is sent only when false, so the API keeps choosing the pair where
// the zone has two compute nodes. A crash between the two calls leaves a load balancer with the controller's
// deterministic name, which the provider adopts by name on its next pass.
func (client *Client) CreateLoadBalancer(ctx context.Context, input CreateLoadBalancerInput) (LoadBalancer, error) {
	body := createLoadBalancerBody{Zone: input.Zone, Name: input.Name, NetworkID: input.NetworkID, Labels: input.Labels, PublicIPv4: input.PublicIPv4}
	if !input.HighAvailability {
		isHighlyAvailable := false
		body.HighAvailability = &isHighlyAvailable
	}
	encoded, encodeError := json.Marshal(body)
	if encodeError != nil {
		return LoadBalancer{}, fmt.Errorf("create_load_balancer: encode the body: %w", encodeError)
	}
	answer, callError := client.generated.Call(ctx, "create_load_balancer", nil, nil, encoded)
	if callError != nil {
		return LoadBalancer{}, translateError(callError)
	}
	balancer, decodeError := decodeLoadBalancerEnvelope("create_load_balancer", answer.Body)
	if decodeError != nil {
		return LoadBalancer{}, decodeError
	}
	if !carriesLabels(balancer.Labels, input.Labels) {
		if updateError := client.UpdateLoadBalancer(ctx, balancer.ID, UpdateLoadBalancerInput{Labels: input.Labels}); updateError != nil {
			return balancer, fmt.Errorf("label load balancer %s: %w", balancer.ID, updateError)
		}
	}
	if len(input.Labels) > 0 {
		balancer.Labels = input.Labels
	}
	return balancer, nil
}

func carriesLabels(actual map[string]string, expected map[string]string) bool {
	for key, value := range expected {
		if actualValue, isSet := actual[key]; !isSet || actualValue != value {
			return false
		}
	}
	return true
}

type updateLoadBalancerBody struct {
	Name   *string           `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// UpdateLoadBalancer calls update_load_balancer (PATCH /v1/load-balancers/{id}).
func (client *Client) UpdateLoadBalancer(ctx context.Context, loadBalancerID string, input UpdateLoadBalancerInput) error {
	encoded, encodeError := json.Marshal(updateLoadBalancerBody(input))
	if encodeError != nil {
		return fmt.Errorf("update_load_balancer: encode the body: %w", encodeError)
	}
	_, callError := client.generated.Call(ctx, "update_load_balancer", map[string]string{"id": loadBalancerID}, nil, encoded)
	return translateError(callError)
}

// DeleteLoadBalancer calls delete_load_balancer.
func (client *Client) DeleteLoadBalancer(ctx context.Context, loadBalancerID string) error {
	_, callError := client.generated.DeleteLoadBalancer(ctx, ankraapi.DeleteLoadBalancerParameters{ID: loadBalancerID})
	return translateError(callError)
}

func healthCheckInput(check HealthCheck) *ankraapi.LoadBalancerHealthCheckInput {
	input := &ankraapi.LoadBalancerHealthCheckInput{}
	isSet := false
	if check.Type != "" {
		input.Type = &check.Type
		isSet = true
	}
	if check.Path != "" {
		input.Path = &check.Path
		isSet = true
	}
	if check.ExpectedStatus != "" {
		input.ExpectedStatus = &check.ExpectedStatus
		isSet = true
	}
	if check.IntervalSeconds > 0 {
		input.IntervalSeconds = &check.IntervalSeconds
		isSet = true
	}
	if check.Rise > 0 {
		input.Rise = &check.Rise
		isSet = true
	}
	if check.Fall > 0 {
		input.Fall = &check.Fall
		isSet = true
	}
	if !isSet {
		return nil
	}
	return input
}

// CreateBackend calls create_load_balancer_backend.
func (client *Client) CreateBackend(ctx context.Context, loadBalancerID string, input BackendInput) (Backend, error) {
	answer, callError := client.generated.CreateLoadBalancerBackend(ctx, ankraapi.CreateLoadBalancerBackendParameters{ID: loadBalancerID},
		ankraapi.CreateLoadBalancerBackendRequest{Name: input.Name, Mode: input.Mode, HealthCheck: healthCheckInput(input.HealthCheck)})
	if callError != nil {
		return Backend{}, translateError(callError)
	}
	return backendFromAPI(answer.Backend), nil
}

// UpdateBackend calls update_load_balancer_backend with the health check.
func (client *Client) UpdateBackend(ctx context.Context, loadBalancerID string, backendID string, input BackendInput) error {
	_, callError := client.generated.UpdateLoadBalancerBackend(ctx, ankraapi.UpdateLoadBalancerBackendParameters{ID: loadBalancerID, Backend: backendID},
		ankraapi.UpdateLoadBalancerBackendRequest{HealthCheck: healthCheckInput(input.HealthCheck)})
	return translateError(callError)
}

// DeleteBackend calls delete_load_balancer_backend.
func (client *Client) DeleteBackend(ctx context.Context, loadBalancerID string, backendID string) error {
	return translateError(client.generated.DeleteLoadBalancerBackend(ctx, ankraapi.DeleteLoadBalancerBackendParameters{ID: loadBalancerID, Backend: backendID}))
}

// CreateFrontend calls create_load_balancer_frontend.
func (client *Client) CreateFrontend(ctx context.Context, loadBalancerID string, input FrontendInput) (Frontend, error) {
	answer, callError := client.generated.CreateLoadBalancerFrontend(ctx, ankraapi.CreateLoadBalancerFrontendParameters{ID: loadBalancerID},
		ankraapi.CreateLoadBalancerFrontendRequest{Name: input.Name, Port: input.Port, BackendID: input.BackendID})
	if callError != nil {
		return Frontend{}, translateError(callError)
	}
	return Frontend{ID: answer.Frontend.ID, Name: answer.Frontend.Name, Port: answer.Frontend.Port, BackendID: answer.Frontend.BackendID}, nil
}

// DeleteFrontend calls delete_load_balancer_frontend.
func (client *Client) DeleteFrontend(ctx context.Context, loadBalancerID string, frontendID string) error {
	return translateError(client.generated.DeleteLoadBalancerFrontend(ctx, ankraapi.DeleteLoadBalancerFrontendParameters{ID: loadBalancerID, Frontend: frontendID}))
}

type memberBody struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int64  `json:"port"`
}

type replaceMembersBody struct {
	Members []memberBody `json:"members"`
}

// ReplaceMembers calls replace_load_balancer_members (PUT the backend's whole member list).
func (client *Client) ReplaceMembers(ctx context.Context, loadBalancerID string, backendID string, members []Member) error {
	body := replaceMembersBody{Members: make([]memberBody, 0, len(members))}
	for _, member := range members {
		body.Members = append(body.Members, memberBody(member))
	}
	encoded, encodeError := json.Marshal(body)
	if encodeError != nil {
		return fmt.Errorf("replace_load_balancer_members: encode the body: %w", encodeError)
	}
	_, callError := client.generated.Call(ctx, "replace_load_balancer_members", map[string]string{"id": loadBalancerID, "backend": backendID}, nil, encoded)
	return translateError(callError)
}

func edgeFromAPI(edge ankraapi.Edge) Edge {
	converted := Edge{
		ID: edge.ID, Zone: edge.Zone, NetworkID: edge.NetworkID, Placement: edge.Placement, State: edge.State,
		Roles: edge.Roles, PublicIPv4: stringValue(edge.PublicIPv4),
	}
	for _, machine := range edge.Machines {
		converted.Machines = append(converted.Machines, EdgeMachine{Roles: machine.Roles, PublicIPv6: stringValue(machine.PublicIPv6), PublicIPv4: stringValue(machine.PublicIPv4)})
	}
	for _, member := range edge.Members {
		converted.Members = append(converted.Members, EdgeMember{
			Name: member.Name, FrontendPort: member.FrontendPort, Address: member.Address, Port: member.Port, Weight: member.Weight, Enabled: member.Enabled,
		})
	}
	return converted
}

// GetEdge calls get_edge.
func (client *Client) GetEdge(ctx context.Context, edgeID string) (Edge, error) {
	answer, callError := client.generated.GetEdge(ctx, ankraapi.GetEdgeParameters{ID: edgeID})
	if callError != nil {
		return Edge{}, translateError(callError)
	}
	return edgeFromAPI(answer.Edge), nil
}

// ReplaceEdgeMembers calls update_edge with the edge's whole member list. An empty list goes through Call with an
// explicit `"members": []`, since the generated request drops an empty slice and would leave the members in place.
func (client *Client) ReplaceEdgeMembers(ctx context.Context, edgeID string, members []EdgeMember) error {
	if len(members) == 0 {
		_, callError := client.generated.Call(ctx, "update_edge", map[string]string{"id": edgeID}, nil, []byte(`{"members":[]}`))
		return translateError(callError)
	}
	inputs := make([]ankraapi.EdgeMemberInput, 0, len(members))
	for _, member := range members {
		weight := member.Weight
		isEnabled := member.Enabled
		inputs = append(inputs, ankraapi.EdgeMemberInput{
			Name: member.Name, FrontendPort: member.FrontendPort, Address: member.Address, Port: member.Port, Weight: &weight, Enabled: &isEnabled,
		})
	}
	_, callError := client.generated.UpdateEdge(ctx, ankraapi.UpdateEdgeParameters{ID: edgeID}, ankraapi.UpdateEdgeRequest{Members: inputs})
	return translateError(callError)
}
