package ankraapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type recordedRequest struct {
	method, path, query, authorization, cookie, csrf, contentType, body string
}

func newRecordingServer(t *testing.T, status int, answer string) (*httptest.Server, *recordedRequest) {
	t.Helper()
	recorded := &recordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		cookie, _ := request.Cookie("ankracloud_session")
		staffCookie, _ := request.Cookie("ankracloud_admin_session")
		*recorded = recordedRequest{method: request.Method, path: request.URL.EscapedPath(), query: request.URL.RawQuery,
			authorization: request.Header.Get("Authorization"), csrf: request.Header.Get("X-CSRF-Token"), contentType: request.Header.Get("Content-Type"), body: string(body)}
		if cookie != nil {
			recorded.cookie = cookie.Value
		}
		if staffCookie != nil {
			recorded.cookie = "staff:" + staffCookie.Value
		}
		if status >= 400 {
			writer.Header().Set("Content-Type", "application/problem+json")
		} else {
			writer.Header().Set("Content-Type", "application/json")
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, answer)
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

func TestTypedMethodSendsParametersAndDecodesTheAnswer(t *testing.T) {
	server, recorded := newRecordingServer(t, http.StatusOK, `{"items":[{"id":"server-1","hostname":"web-1"}],"next_cursor":null}`)
	client, newError := New(server.URL, APITokenCredential("act_secret"))
	if newError != nil {
		t.Fatal(newError)
	}
	limit := int64(10)
	listed, listError := client.ListServers(context.Background(), ListServersParameters{Limit: &limit})
	if listError != nil || len(listed.Items) != 1 || listed.Items[0].Hostname != "web-1" {
		t.Fatalf("list = %+v %v", listed, listError)
	}
	if recorded.method != http.MethodGet || recorded.path != "/v1/servers" || recorded.query != "limit=10" || recorded.authorization != "Bearer act_secret" {
		t.Fatalf("request = %+v", recorded)
	}
}

func TestPathParametersAreEscapedAndBodiesAreJSON(t *testing.T) {
	server, recorded := newRecordingServer(t, http.StatusOK, `{"server":{"id":"a/b","hostname":"web-2"}}`)
	client, _ := New(server.URL, SessionCredential("acs_session", "csrf"))
	title := "Web"
	updated, updateError := client.UpdateServer(context.Background(), UpdateServerParameters{ID: "a/b"}, UpdateServerRequest{Title: &title})
	if updateError != nil || updated.Server.Hostname != "web-2" {
		t.Fatalf("update = %+v %v", updated, updateError)
	}
	if recorded.method != http.MethodPatch || recorded.path != "/v1/servers/a%2Fb" || recorded.cookie != "acs_session" || recorded.csrf != "csrf" ||
		recorded.contentType != "application/json" || !strings.Contains(recorded.body, `"title":"Web"`) {
		t.Fatalf("request = %+v", recorded)
	}
}

func TestProblemAnswersBecomeErrors(t *testing.T) {
	server, _ := newRecordingServer(t, http.StatusConflict, `{"type":"about:blank","title":"Conflict","status":409,"detail":"the server is busy"}`)
	client, _ := New(server.URL, OperatorTokenCredential("aop_secret"))
	_, releaseError := client.ForceReleaseZoneServer(context.Background(), ForceReleaseZoneServerParameters{Zone: "de-fsn1", ID: "server-1"},
		ForceReleaseZoneServerRequest{Reason: "node burned"})
	var apiError *Error
	if !errors.As(releaseError, &apiError) || apiError.StatusCode != http.StatusConflict || apiError.Detail != "the server is busy" {
		t.Fatalf("error = %v", releaseError)
	}
}

func TestCallRunsAnyOperationByItsIdentifier(t *testing.T) {
	server, recorded := newRecordingServer(t, http.StatusOK, `{"staff":{"id":"staff-2","role":"admin"}}`)
	client, _ := New(server.URL, StaffSessionCredential("ass_staff", "staff-csrf"))
	answer, callError := client.Call(context.Background(), "update_staff", map[string]string{"id": "staff-2"}, url.Values{}, []byte(`{"role":"admin"}`))
	if callError != nil || answer.StatusCode != http.StatusOK || !strings.Contains(string(answer.Body), `"role":"admin"`) {
		t.Fatalf("call = %+v %v", answer, callError)
	}
	if recorded.method != http.MethodPatch || recorded.path != "/admin/v1/staff/staff-2" || recorded.cookie != "staff:ass_staff" || recorded.csrf != "staff-csrf" {
		t.Fatalf("request = %+v", recorded)
	}
	if _, unknownError := client.Call(context.Background(), "launch_rockets", nil, nil, nil); !errors.Is(unknownError, ErrUnknownOperation) {
		t.Fatalf("unknown operation = %v", unknownError)
	}
	if _, missingError := client.Call(context.Background(), "update_staff", nil, nil, nil); missingError == nil || !strings.Contains(missingError.Error(), "path parameter id") {
		t.Fatalf("missing path parameter = %v", missingError)
	}
}

func TestTheOperationTableKnowsWhoMayCallEachOperation(t *testing.T) {
	identifiers := OperationIDs()
	if len(identifiers) != len(Operations) || identifiers[0] > identifiers[len(identifiers)-1] {
		t.Fatalf("operation ids = %d of %d", len(identifiers), len(Operations))
	}
	if !Operations["list_servers"].AcceptsAPIToken() || Operations["create_api_token"].AcceptsAPIToken() || Operations["list_staff"].StaffRole != "admin" ||
		Operations["drain_zone_node"].OperatorScope != "maintenance" || Operations["get_database_credentials"].IsCredentialRead != true {
		t.Fatal("the operation table lost the guard extensions")
	}
	if _, newError := New("ftp://example.com", AnonymousCredential()); newError == nil {
		t.Fatal("only http and https endpoints are accepted")
	}
	if _, newError := New("https://api.example.com", nil); newError == nil {
		t.Fatal("a credential is required")
	}
}
