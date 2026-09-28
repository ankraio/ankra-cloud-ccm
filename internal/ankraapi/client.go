// Package ankraapi is the Go client of the Ankra Cloud API: one typed method per operation (named after its
// operationId), a type per schema, and Call for any operation by its operationId. operations_gen.go is generated from
// the Ankra Cloud OpenAPI document by `make client` (tools/openapigen); this file is the hand-written transport.
package ankraapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// OperationSpec describes one operation of the API.
type OperationSpec struct {
	ID      string
	Method  string
	Path    string
	Tag     string
	Summary string
	// Security lists the accepted credentials, each as its scheme names joined with "+" ("apiToken",
	// "csrfHeader+sessionCookie", "operatorToken", "staffCsrfHeader+staffSession"); "" means no credential.
	Security        []string
	PathParameters  []string
	QueryParameters []string
	// BodyMediaType is the request body's media type, empty for an operation without a body.
	BodyMediaType    string
	Permission       string
	OperatorScope    string
	StaffRole        string
	IsCredentialRead bool
}

// AcceptsAPIToken reports whether an API token may call the operation.
func (operation OperationSpec) AcceptsAPIToken() bool {
	for _, requirement := range operation.Security {
		if requirement == "apiToken" {
			return true
		}
	}
	return false
}

// OperationIDs returns every operationId, sorted.
func OperationIDs() []string {
	identifiers := make([]string, 0, len(Operations))
	for identifier := range Operations {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	return identifiers
}

// Credential authenticates the client's requests.
type Credential interface {
	apply(request *http.Request)
}

type bearer string

func (token bearer) apply(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+string(token))
}

type cookieSession struct {
	cookieName string
	token      string
	csrfToken  string
}

func (session cookieSession) apply(request *http.Request) {
	request.AddCookie(&http.Cookie{Name: session.cookieName, Value: session.token})
	if request.Method != http.MethodGet && request.Method != http.MethodHead && session.csrfToken != "" {
		request.Header.Set("X-CSRF-Token", session.csrfToken)
	}
}

type anonymous struct{}

func (anonymous) apply(*http.Request) {}

// APITokenCredential authenticates as a customer API token (`act_…`).
func APITokenCredential(token string) Credential { return bearer(token) }

// OperatorTokenCredential authenticates as a named operator token.
func OperatorTokenCredential(token string) Credential { return bearer(token) }

// SessionCredential authenticates with a portal session and its CSRF token.
func SessionCredential(sessionToken string, csrfToken string) Credential {
	return cookieSession{cookieName: "ankracloud_session", token: sessionToken, csrfToken: csrfToken}
}

// StaffSessionCredential authenticates with an admin console staff session and its CSRF token.
func StaffSessionCredential(sessionToken string, csrfToken string) Credential {
	return cookieSession{cookieName: "ankracloud_admin_session", token: sessionToken, csrfToken: csrfToken}
}

// AnonymousCredential sends no credential, for the public operations.
func AnonymousCredential() Credential { return anonymous{} }

// Client calls the API with one credential.
type Client struct {
	endpoint   *url.URL
	credential Credential
	httpClient *http.Client
	userAgent  string
}

// Option configures a Client.
type Option func(client *Client)

// WithHTTPClient replaces the HTTP client (timeouts, TLS, proxies). The client never follows redirects itself, so
// an operation that answers 302 returns the redirect.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) {
		copied := *httpClient
		copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client.httpClient = &copied
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(client *Client) { client.userAgent = userAgent }
}

// New builds a client for endpoint (for example https://api.ankra.cloud).
func New(endpoint string, credential Credential, options ...Option) (*Client, error) {
	parsed, parseError := url.Parse(strings.TrimRight(endpoint, "/"))
	if parseError != nil {
		return nil, fmt.Errorf("parse endpoint: %w", parseError)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("endpoint %q must be an http or https URL with a host", endpoint)
	}
	if credential == nil {
		return nil, errors.New("a credential is required; use AnonymousCredential() for the public operations")
	}
	client := &Client{endpoint: parsed, credential: credential, userAgent: "ankra-cloud-openapi-client",
		httpClient: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

// Error is an answer outside 2xx and 3xx: the RFC 7807 problem document the API returned.
type Error struct {
	StatusCode int
	Title      string
	Detail     string
	// Body is the raw answer, for problem documents that carry more (a coupon refusal's reason).
	Body []byte
}

func (apiError *Error) Error() string {
	if apiError.Detail != "" {
		return fmt.Sprintf("%d %s: %s", apiError.StatusCode, apiError.Title, apiError.Detail)
	}
	return fmt.Sprintf("%d %s", apiError.StatusCode, apiError.Title)
}

// RawResponse is an answer the client does not decode: a redirect, a stream, text or protobuf.
type RawResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// ErrUnknownOperation is returned by Call for an operationId the API does not have.
var ErrUnknownOperation = errors.New("unknown operation")

// Call runs any operation by its operationId: the generic entry point the CLI and tools use. body is sent as is with
// the operation's media type; the answer is returned undecoded.
func (client *Client) Call(ctx context.Context, operationID string, pathParameters map[string]string, query url.Values, body []byte) (RawResponse, error) {
	operation, isKnown := Operations[operationID]
	if !isKnown {
		return RawResponse{}, fmt.Errorf("%w %q", ErrUnknownOperation, operationID)
	}
	var payload any
	if body != nil {
		payload = rawBody(body)
	}
	return client.send(ctx, operation, pathParameters, query, payload)
}

// rawBody is a request body sent as given.
type rawBody []byte

// expandPath fills the operation's path parameters.
func expandPath(operation OperationSpec, pathParameters map[string]string) (string, error) {
	path := operation.Path
	for _, name := range operation.PathParameters {
		value, isPresent := pathParameters[name]
		if !isPresent || value == "" {
			return "", fmt.Errorf("%s needs the path parameter %s", operation.ID, name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	return path, nil
}

func (client *Client) send(ctx context.Context, operation OperationSpec, pathParameters map[string]string, query url.Values, body any) (RawResponse, error) {
	path, pathError := expandPath(operation, pathParameters)
	if pathError != nil {
		return RawResponse{}, pathError
	}
	target := client.endpoint.JoinPath(path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}
	var payload io.Reader
	switch typed := body.(type) {
	case nil:
	case rawBody:
		payload = bytes.NewReader(typed)
	case []byte:
		payload = bytes.NewReader(typed)
	default:
		encoded, encodeError := json.Marshal(body)
		if encodeError != nil {
			return RawResponse{}, fmt.Errorf("%s: encode the body: %w", operation.ID, encodeError)
		}
		payload = bytes.NewReader(encoded)
	}
	request, requestError := http.NewRequestWithContext(ctx, operation.Method, target.String(), payload)
	if requestError != nil {
		return RawResponse{}, fmt.Errorf("%s: build the request: %w", operation.ID, requestError)
	}
	if payload != nil {
		mediaType := operation.BodyMediaType
		if mediaType == "" {
			mediaType = "application/json"
		}
		request.Header.Set("Content-Type", mediaType)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	client.credential.apply(request)
	response, sendError := client.httpClient.Do(request)
	if sendError != nil {
		return RawResponse{}, fmt.Errorf("%s: %w", operation.ID, sendError)
	}
	defer func() { _ = response.Body.Close() }()
	content, readError := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if readError != nil {
		return RawResponse{}, fmt.Errorf("%s: read the answer: %w", operation.ID, readError)
	}
	answer := RawResponse{StatusCode: response.StatusCode, Header: response.Header, Body: content}
	if response.StatusCode >= 400 {
		apiError := &Error{StatusCode: response.StatusCode, Title: http.StatusText(response.StatusCode), Body: content}
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
		return answer, apiError
	}
	return answer, nil
}

// decode sends the request and decodes a JSON answer into result.
func (client *Client) decode(ctx context.Context, operation OperationSpec, pathParameters map[string]string, query url.Values, body any, result any) error {
	answer, sendError := client.send(ctx, operation, pathParameters, query, body)
	if sendError != nil {
		return sendError
	}
	if len(bytes.TrimSpace(answer.Body)) == 0 {
		return nil
	}
	if decodeError := json.Unmarshal(answer.Body, result); decodeError != nil {
		return fmt.Errorf("%s: decode the answer: %w", operation.ID, decodeError)
	}
	return nil
}
