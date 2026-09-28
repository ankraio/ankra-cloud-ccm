package cloudapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/ankraio/ankra-cloud-ccm/internal/ankraapi"
)

// pendingOperation is an operation the controller calls before the generated client knows it. Once the Ankra Cloud
// OpenAPI document declares the operationId and `make sync-client` regenerates the client, the call goes through the
// generated client's Call instead; until then it is a thin HTTP request with the same credential.
type pendingOperation struct {
	operationID string
	method      string
	path        string
}

var (
	updateLoadBalancerOperation = pendingOperation{
		operationID: "update_load_balancer", method: http.MethodPatch, path: "/v1/load-balancers/{id}",
	}
	replaceLoadBalancerMembersOperation = pendingOperation{
		operationID: "replace_load_balancer_members", method: http.MethodPut, path: "/v1/load-balancers/{id}/backends/{backend}/members",
	}
	getZoneCapabilitiesOperation = pendingOperation{
		operationID: "get_zone_capabilities", method: http.MethodGet, path: "/v1/zones/{zone}/capabilities",
	}
)

// Client implements API on the generated OpenAPI client.
type Client struct {
	generated  *ankraapi.Client
	endpoint   *url.URL
	token      string
	httpClient *http.Client
	userAgent  string
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
	parsed, parseError := url.Parse(strings.TrimRight(endpoint, "/"))
	if parseError != nil {
		return nil, fmt.Errorf("parse endpoint: %w", parseError)
	}
	return &Client{generated: generated, endpoint: parsed, token: token, httpClient: httpClient, userAgent: userAgent}, nil
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

// callPending runs a pending operation: through the generated client when it already knows the operationId,
// otherwise as a direct HTTP request. It returns the undecoded answer body.
func (client *Client) callPending(ctx context.Context, operation pendingOperation, pathParameters map[string]string, body any) ([]byte, error) {
	var encoded []byte
	if body != nil {
		marshalled, marshalError := json.Marshal(body)
		if marshalError != nil {
			return nil, fmt.Errorf("%s: encode the body: %w", operation.operationID, marshalError)
		}
		encoded = marshalled
	}
	if _, isKnown := ankraapi.Operations[operation.operationID]; isKnown {
		answer, callError := client.generated.Call(ctx, operation.operationID, pathParameters, nil, encoded)
		if callError != nil {
			return nil, translateError(callError)
		}
		return answer.Body, nil
	}
	path := operation.path
	for name, value := range pathParameters {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	var payload io.Reader
	if encoded != nil {
		payload = bytes.NewReader(encoded)
	}
	request, requestError := http.NewRequestWithContext(ctx, operation.method, client.endpoint.JoinPath(path).String(), payload)
	if requestError != nil {
		return nil, fmt.Errorf("%s: build the request: %w", operation.operationID, requestError)
	}
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	request.Header.Set("Authorization", "Bearer "+client.token)
	response, sendError := client.httpClient.Do(request)
	if sendError != nil {
		return nil, fmt.Errorf("%s: %w", operation.operationID, sendError)
	}
	defer func() { _ = response.Body.Close() }()
	content, readError := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if readError != nil {
		return nil, fmt.Errorf("%s: read the answer: %w", operation.operationID, readError)
	}
	if response.StatusCode >= 400 {
		apiError := &ankraapi.Error{StatusCode: response.StatusCode, Title: http.StatusText(response.StatusCode), Body: content}
		var problem struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(content, &problem) == nil {
			if problem.Title != "" {
				apiError.Title = problem.Title
			}
			apiError.Detail = problem.Detail
		}
		return nil, translateError(fmt.Errorf("%s: %w", operation.operationID, apiError))
	}
	return content, nil
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

type zoneCapabilitiesDocument struct {
	Stage    string `json:"stage"`
	Servers  int    `json:"servers"`
	Features struct {
		LoadBalancerHA bool `json:"load_balancer_ha"`
	} `json:"features"`
}

// GetZoneCapabilities calls get_zone_capabilities. An API without it (404, 405) or one that keeps it
// from API tokens (401, 403) answers IsKnown false rather than an error.
func (client *Client) GetZoneCapabilities(ctx context.Context, zone string) (ZoneCapabilities, error) {
	content, callError := client.callPending(ctx, getZoneCapabilitiesOperation, map[string]string{"zone": zone}, nil)
	if callError != nil {
		switch statusCodeOf(callError) {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized, http.StatusForbidden:
			return ZoneCapabilities{}, nil
		}
		return ZoneCapabilities{}, callError
	}
	var document zoneCapabilitiesDocument
	if decodeError := json.Unmarshal(content, &document); decodeError != nil {
		return ZoneCapabilities{}, fmt.Errorf("get_zone_capabilities: decode the answer: %w", decodeError)
	}
	return ZoneCapabilities{IsKnown: true, Stage: document.Stage, LoadBalancerHA: document.Features.LoadBalancerHA, ComputeNodeCount: document.Servers}, nil
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

// ListLoadBalancers calls list_load_balancers (without frontends and backends).
func (client *Client) ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	answer, callError := client.generated.Call(ctx, "list_load_balancers", nil, nil, nil)
	if callError != nil {
		return nil, translateError(callError)
	}
	var list struct {
		Items []loadBalancerDocument `json:"items"`
	}
	if decodeError := json.Unmarshal(answer.Body, &list); decodeError != nil {
		return nil, fmt.Errorf("list_load_balancers: decode the answer: %w", decodeError)
	}
	balancers := make([]LoadBalancer, 0, len(list.Items))
	for _, item := range list.Items {
		balancers = append(balancers, item.toLoadBalancer())
	}
	return balancers, nil
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
	Zone             string `json:"zone"`
	Name             string `json:"name"`
	NetworkID        string `json:"network_id,omitempty"`
	HighAvailability *bool  `json:"high_availability,omitempty"`
	PublicIPv4       *bool  `json:"public_ipv4,omitempty"`
}

// CreateLoadBalancer calls create_load_balancer, then update_load_balancer for the labels (create takes none).
// A crash between the two leaves a load balancer with the controller's deterministic name and no labels, which
// the provider adopts by name on its next pass.
func (client *Client) CreateLoadBalancer(ctx context.Context, input CreateLoadBalancerInput) (LoadBalancer, error) {
	body := createLoadBalancerBody{Zone: input.Zone, Name: input.Name, NetworkID: input.NetworkID}
	if !input.HighAvailability {
		isHighlyAvailable := false
		body.HighAvailability = &isHighlyAvailable
	}
	if input.PublicIPv4 {
		hasPublicIPv4 := true
		body.PublicIPv4 = &hasPublicIPv4
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
	if len(input.Labels) > 0 {
		if updateError := client.UpdateLoadBalancer(ctx, balancer.ID, UpdateLoadBalancerInput{Labels: input.Labels}); updateError != nil {
			return balancer, fmt.Errorf("label load balancer %s: %w", balancer.ID, updateError)
		}
		balancer.Labels = input.Labels
	}
	return balancer, nil
}

type updateLoadBalancerBody struct {
	Name   *string           `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// UpdateLoadBalancer calls update_load_balancer (PATCH /v1/load-balancers/{id}).
func (client *Client) UpdateLoadBalancer(ctx context.Context, loadBalancerID string, input UpdateLoadBalancerInput) error {
	_, callError := client.callPending(ctx, updateLoadBalancerOperation, map[string]string{"id": loadBalancerID},
		updateLoadBalancerBody(input))
	return callError
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
	_, callError := client.callPending(ctx, replaceLoadBalancerMembersOperation, map[string]string{"id": loadBalancerID, "backend": backendID}, body)
	return callError
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
