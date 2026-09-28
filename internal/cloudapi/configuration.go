package cloudapi

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Environment variables the controller reads its API access from (the chart fills them from a Secret).
const (
	TokenEnvironmentVariable    = "ANKRA_CLOUD_TOKEN"
	APIURLEnvironmentVariable   = "ANKRA_CLOUD_API_URL"
	CABundleEnvironmentVariable = "ANKRA_CLOUD_CA_BUNDLE"
)

// Configuration is how the controller reaches the API.
type Configuration struct {
	APIURL string
	Token  string
	// CABundlePath is a PEM file of extra certificate authorities to trust, for an API behind a private CA.
	CABundlePath string
}

// ConfigurationFromEnvironment reads the Configuration from the process environment through lookup.
func ConfigurationFromEnvironment(lookup func(string) (string, bool)) (Configuration, error) {
	configuration := Configuration{}
	if value, isSet := lookup(TokenEnvironmentVariable); isSet {
		configuration.Token = strings.TrimSpace(value)
	}
	if value, isSet := lookup(APIURLEnvironmentVariable); isSet {
		configuration.APIURL = strings.TrimSpace(value)
	}
	if value, isSet := lookup(CABundleEnvironmentVariable); isSet {
		configuration.CABundlePath = strings.TrimSpace(value)
	}
	var missing []string
	if configuration.Token == "" {
		missing = append(missing, TokenEnvironmentVariable)
	}
	if configuration.APIURL == "" {
		missing = append(missing, APIURLEnvironmentVariable)
	}
	if len(missing) > 0 {
		return Configuration{}, fmt.Errorf("set %s", strings.Join(missing, " and "))
	}
	return configuration, nil
}

// HTTPClient builds the HTTP client for the API: a timeout and, with a CA bundle, the system roots plus the bundle.
func (configuration Configuration) HTTPClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if configuration.CABundlePath != "" {
		bundle, readError := os.ReadFile(configuration.CABundlePath)
		if readError != nil {
			return nil, fmt.Errorf("read the CA bundle: %w", readError)
		}
		roots, systemError := x509.SystemCertPool()
		if systemError != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(bundle) {
			return nil, errors.New("the CA bundle holds no PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}
