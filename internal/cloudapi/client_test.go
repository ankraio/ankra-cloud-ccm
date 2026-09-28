package cloudapi

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordedRequest struct {
	method        string
	path          string
	query         string
	body          string
	authorization string
}

type fakeServer struct {
	lock      sync.Mutex
	requests  []recordedRequest
	responses map[string]func(writer http.ResponseWriter)
}

func (server *fakeServer) handler(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	server.lock.Lock()
	server.requests = append(server.requests, recordedRequest{
		method: request.Method, path: request.URL.Path, query: request.URL.RawQuery, body: string(body),
		authorization: request.Header.Get("Authorization"),
	})
	respond, isKnown := server.responses[request.Method+" "+request.URL.Path]
	server.lock.Unlock()
	if !isKnown {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"title":"Not Found","detail":"no such resource"}`))
		return
	}
	respond(writer)
}

func jsonAnswer(status int, body string) func(writer http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}
}

func newTestClient(t *testing.T, responses map[string]func(writer http.ResponseWriter)) (*Client, *fakeServer) {
	t.Helper()
	server := &fakeServer{responses: responses}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	t.Cleanup(httpServer.Close)
	client, clientError := NewClient(httpServer.URL, "act_test", httpServer.Client(), "ankra-cloud-ccm/test")
	if clientError != nil {
		t.Fatal(clientError)
	}
	return client, server
}

const loadBalancerJSON = `{"id":"lb-1","zone":"de-fsn1","name":"k8s-production-shop-web","network_id":"network-1","state":"running",
"address":"203.0.113.5","configuration_generation":3,"is_configuration_applied":true,"nodes":[],"active_operation":null,
"created_at":"2026-09-28T10:00:00Z","labels":{"ccm.ankra.cloud/service":"shop/web"},"public_ipv6":"fd64:1:0:9::1",
"frontends":[{"id":"frontend-1","name":"tcp-80","port":80,"mode":"tcp","backend_id":"backend-1","tls":false,"certificate_ids":[],"redirect_to_https":false}],
"backends":[{"id":"backend-1","name":"tcp-80","mode":"tcp","balance":"roundrobin","health_check_path":"",
"health_check":{"type":"tcp","path":"/","expected_status":"200-399","interval_seconds":2,"rise":2,"fall":3},
"members":[{"id":"member-1","backend_id":"backend-1","name":"worker-1-1a2b3c4d","address":"fd12::5","port":30080,"weight":100,"enabled":true}]}]}`

func TestGetLoadBalancerReadsBothFamilies(t *testing.T) {
	client, server := newTestClient(t, map[string]func(http.ResponseWriter){
		"GET /v1/load-balancers/lb-1": jsonAnswer(http.StatusOK, `{"load_balancer":`+loadBalancerJSON+`}`),
	})
	balancer, getError := client.GetLoadBalancer(context.Background(), "lb-1")
	if getError != nil {
		t.Fatal(getError)
	}
	if balancer.PublicIPv6 != "fd64:1:0:9::1" || balancer.PublicIPv4 != "203.0.113.5" || balancer.Labels["ccm.ankra.cloud/service"] != "shop/web" {
		t.Fatalf("load balancer %+v", balancer)
	}
	if len(balancer.Backends) != 1 || balancer.Backends[0].HealthCheck.Fall != 3 || balancer.Backends[0].Members[0].Port != 30080 {
		t.Fatalf("backends %+v", balancer.Backends)
	}
	if server.requests[0].authorization != "Bearer act_test" {
		t.Fatalf("authorization %q", server.requests[0].authorization)
	}
}

func TestAnIPv6OnlyLegacyAddressIsReadAsIPv6(t *testing.T) {
	document := loadBalancerDocument{Address: new(string)}
	*document.Address = "fd64:1:0:9::1"
	balancer := document.toLoadBalancer()
	if balancer.PublicIPv6 != "fd64:1:0:9::1" || balancer.PublicIPv4 != "" {
		t.Fatalf("load balancer %+v", balancer)
	}
}

func TestNotFoundIsErrNotFound(t *testing.T) {
	client, _ := newTestClient(t, nil)
	if _, getError := client.GetServer(context.Background(), "server-9"); !errors.Is(getError, ErrNotFound) {
		t.Fatalf("error %v", getError)
	}
	if deleteError := client.DeleteLoadBalancer(context.Background(), "lb-9"); !errors.Is(deleteError, ErrNotFound) {
		t.Fatalf("error %v", deleteError)
	}
}

func TestCreateLoadBalancerSendsOnlyNonDefaultFieldsThenLabels(t *testing.T) {
	answer := jsonAnswer(http.StatusAccepted, `{"load_balancer":`+loadBalancerJSON+`,"operation":{"id":"operation-1","kind":"load_balancer.create","status":"pending","step":"","step_count":0,"step_index":0,"error":"","created_at":"2026-09-28T10:00:00Z","deadline_at":"2026-09-28T11:00:00Z","started_at":null,"finished_at":null,"server_id":null,"router_id":null,"storage_id":null,"floating_ip_id":null,"template_id":null,"backup_id":null}}`)
	client, server := newTestClient(t, map[string]func(http.ResponseWriter){
		"POST /v1/load-balancers":       answer,
		"PATCH /v1/load-balancers/lb-1": jsonAnswer(http.StatusOK, `{"load_balancer":`+loadBalancerJSON+`}`),
	})
	labels := map[string]string{"ccm.ankra.cloud/service": "shop/web"}
	if _, createError := client.CreateLoadBalancer(context.Background(), CreateLoadBalancerInput{
		Name: "k8s-production-shop-web", Zone: "de-fsn1", NetworkID: "network-1", Labels: labels, HighAvailability: true,
	}); createError != nil {
		t.Fatal(createError)
	}
	if _, createError := client.CreateLoadBalancer(context.Background(), CreateLoadBalancerInput{
		Name: "k8s-production-shop-web", Zone: "de-fsn1", HighAvailability: false, PublicIPv4: true,
	}); createError != nil {
		t.Fatal(createError)
	}
	if len(server.requests) != 3 {
		t.Fatalf("requests %+v", server.requests)
	}
	var first map[string]any
	if decodeError := json.Unmarshal([]byte(server.requests[0].body), &first); decodeError != nil {
		t.Fatal(decodeError)
	}
	if len(first) != 3 || first["network_id"] != "network-1" {
		t.Fatalf("default create body %s", server.requests[0].body)
	}
	if server.requests[1].method != http.MethodPatch || server.requests[1].body != `{"labels":{"ccm.ankra.cloud/service":"shop/web"}}` {
		t.Fatalf("labelling request %+v", server.requests[1])
	}
	if server.requests[2].body != `{"zone":"de-fsn1","name":"k8s-production-shop-web","high_availability":false,"public_ipv4":true}` {
		t.Fatalf("single-node IPv4 create body %s", server.requests[2].body)
	}
}

func TestReplaceMembersPutsTheWholeList(t *testing.T) {
	client, server := newTestClient(t, map[string]func(http.ResponseWriter){
		"PUT /v1/load-balancers/lb-1/backends/backend-1/members": jsonAnswer(http.StatusOK, `{"members":[]}`),
	})
	members := []Member{{Name: "worker-1-1a2b3c4d", Address: "fd12::5", Port: 30080}, {Name: "worker-2-5e6f7a8b", Address: "10.0.0.6", Port: 30080}}
	if replaceError := client.ReplaceMembers(context.Background(), "lb-1", "backend-1", members); replaceError != nil {
		t.Fatal(replaceError)
	}
	expected := `{"members":[{"name":"worker-1-1a2b3c4d","address":"fd12::5","port":30080},{"name":"worker-2-5e6f7a8b","address":"10.0.0.6","port":30080}]}`
	if server.requests[0].body != expected {
		t.Fatalf("body %s", server.requests[0].body)
	}
	if replaceError := client.ReplaceMembers(context.Background(), "lb-1", "backend-9", nil); !errors.Is(replaceError, ErrNotFound) {
		t.Fatalf("error %v", replaceError)
	}
	if server.requests[1].body != `{"members":[]}` {
		t.Fatalf("empty body %s", server.requests[1].body)
	}
}

func TestFindServersByHostnameAsksForAnExactMatch(t *testing.T) {
	client, server := newTestClient(t, map[string]func(http.ResponseWriter){
		"GET /v1/servers": jsonAnswer(http.StatusOK, `{"items":[
{"id":"server-1","zone":"de-fsn1","hostname":"worker-1","plan":"standard-2c-4g","state":"started","public_ipv6":"fd64:1:0:2a::1","public_ipv4":null,"public_ipv6_prefix":"fd64:1:0:2a::/64","labels":{}},
{"id":"server-2","zone":"de-fsn1","hostname":"worker-10","plan":"standard-2c-4g","state":"started","public_ipv6":null,"public_ipv4":null,"public_ipv6_prefix":null,"labels":{}}],"next_cursor":null}`),
	})
	servers, findError := client.FindServersByHostname(context.Background(), "worker-1")
	if findError != nil {
		t.Fatal(findError)
	}
	if server.requests[0].query != "hostname=worker-1" {
		t.Fatalf("query %q", server.requests[0].query)
	}
	if len(servers) != 1 || servers[0].ID != "server-1" || servers[0].PublicIPv6 != "fd64:1:0:2a::1" {
		t.Fatalf("servers %+v (an API that ignores hostname= must still match exactly)", servers)
	}
}

func TestZoneCapabilities(t *testing.T) {
	client, _ := newTestClient(t, map[string]func(http.ResponseWriter){
		"GET /v1/zones/de-fsn1/capabilities": jsonAnswer(http.StatusOK, `{"stage":"single","servers":1,"gateways":1,"storage_backends":["ankra-local"],"features":{"live_migration":false,"ha_restart":false,"load_balancer_ha":false,"separate_edges":false}}`),
		"GET /v1/zones/de-fsn9/capabilities": jsonAnswer(http.StatusForbidden, `{"title":"Forbidden"}`),
		"GET /v1/zones/de-fsn3/capabilities": jsonAnswer(http.StatusInternalServerError, `{"title":"Internal Server Error"}`),
	})
	single, singleError := client.GetZoneCapabilities(context.Background(), "de-fsn1")
	if singleError != nil || !single.IsKnown || single.LoadBalancerHA || single.Stage != "single" || single.ComputeNodeCount != 1 {
		t.Fatalf("capabilities %+v, %v", single, singleError)
	}
	for _, zone := range []string{"de-fsn2", "de-fsn9"} {
		unknown, unknownError := client.GetZoneCapabilities(context.Background(), zone)
		if unknownError != nil || unknown.IsKnown {
			t.Fatalf("%s: capabilities %+v, %v", zone, unknown, unknownError)
		}
	}
	if _, failure := client.GetZoneCapabilities(context.Background(), "de-fsn3"); failure == nil {
		t.Fatal("a server error was swallowed")
	}
}

func TestReplaceEdgeMembersClearsWithAnExplicitEmptyList(t *testing.T) {
	edgeAnswer := jsonAnswer(http.StatusAccepted, `{"edge":{"id":"edge-1","name":"edge","zone":"de-fsn1","network_id":"network-1","placement":"combined","plan":"starter-1c-1g","state":"running","roles":["load_balancer"],"machines":[],"members":[],"production_recommended":false,"ssh_key_count":0,"configuration_generation":1,"created_at":"2026-09-28T10:00:00Z","active_operation":null},"operation":{"id":"operation-2","kind":"edge.sync","status":"pending","step":"","step_count":0,"step_index":0,"error":"","created_at":"2026-09-28T10:00:00Z","deadline_at":"2026-09-28T11:00:00Z","started_at":null,"finished_at":null,"server_id":null,"router_id":null,"storage_id":null,"floating_ip_id":null,"template_id":null,"backup_id":null}}`)
	client, server := newTestClient(t, map[string]func(http.ResponseWriter){"PATCH /v1/edges/edge-1": edgeAnswer})
	if replaceError := client.ReplaceEdgeMembers(context.Background(), "edge-1", nil); replaceError != nil {
		t.Fatal(replaceError)
	}
	if server.requests[0].body != `{"members":[]}` {
		t.Fatalf("body %s", server.requests[0].body)
	}
	if replaceError := client.ReplaceEdgeMembers(context.Background(), "edge-1", []EdgeMember{{Name: "k1a2b3c4d-00000001", FrontendPort: 80, Address: "fd12::5", Port: 30080, Weight: 100, Enabled: true}}); replaceError != nil {
		t.Fatal(replaceError)
	}
	if !strings.Contains(server.requests[1].body, `"frontend_port":80`) || !strings.Contains(server.requests[1].body, `"weight":100`) {
		t.Fatalf("body %s", server.requests[1].body)
	}
}

func TestConfigurationFromEnvironment(t *testing.T) {
	values := map[string]string{TokenEnvironmentVariable: " act_x ", APIURLEnvironmentVariable: "https://cloud.ankra.app"}
	lookup := func(key string) (string, bool) {
		value, isSet := values[key]
		return value, isSet
	}
	configuration, configurationError := ConfigurationFromEnvironment(lookup)
	if configurationError != nil || configuration.Token != "act_x" {
		t.Fatalf("configuration %+v, %v", configuration, configurationError)
	}
	delete(values, TokenEnvironmentVariable)
	if _, missingError := ConfigurationFromEnvironment(lookup); missingError == nil || !strings.Contains(missingError.Error(), TokenEnvironmentVariable) {
		t.Fatalf("error %v", missingError)
	}
}

func TestCABundle(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty.pem")
	if writeError := os.WriteFile(empty, []byte("not a certificate"), 0o600); writeError != nil {
		t.Fatal(writeError)
	}
	if _, bundleError := (Configuration{CABundlePath: empty}).HTTPClient(); bundleError == nil {
		t.Fatal("a bundle without certificates was accepted")
	}
	if _, missingError := (Configuration{CABundlePath: filepath.Join(directory, "missing.pem")}).HTTPClient(); missingError == nil {
		t.Fatal("a missing bundle was accepted")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	bundle := filepath.Join(directory, "ca.pem")
	if writeError := os.WriteFile(bundle, certificatePEM(server), 0o600); writeError != nil {
		t.Fatal(writeError)
	}
	client, clientError := (Configuration{CABundlePath: bundle}).HTTPClient()
	if clientError != nil {
		t.Fatal(clientError)
	}
	response, requestError := client.Get(server.URL)
	if requestError != nil {
		t.Fatalf("the private CA was not trusted: %v", requestError)
	}
	_ = response.Body.Close()
}

func certificatePEM(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}
