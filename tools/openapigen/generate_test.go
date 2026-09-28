package main

import (
	"bytes"
	"os"
	"testing"
)

const (
	specificationPath   = "../../api/openapi.yaml"
	generatedClientPath = "../../internal/ankraapi/operations_gen.go"
)

func TestGeneratedClientIsUpToDate(t *testing.T) {
	specification, readError := os.ReadFile(specificationPath)
	if readError != nil {
		t.Fatal(readError)
	}
	generated, generateError := Generate(specification, "ankraapi")
	if generateError != nil {
		t.Fatal(generateError)
	}
	committed, readError := os.ReadFile(generatedClientPath)
	if readError != nil {
		t.Fatal(readError)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatalf("%s is stale: api/openapi.yaml changed; run `make client` and commit the result", generatedClientPath)
	}
}

func TestGoNameFollowsGoInitialisms(t *testing.T) {
	for input, expected := range map[string]string{
		"list_servers":         "ListServers",
		"public_ipv6_prefix":   "PublicIPv6Prefix",
		"server_id":            "ServerID",
		"ssh_command":          "SSHCommand",
		"IdentityProviderConn": "IdentityProviderConn",
		"load-balancers":       "LoadBalancers",
		"certificate_ids":      "CertificateIDs",
		"2fa":                  "Value2fa",
		"x-csrf-token":         "XCSRFToken",
	} {
		if actual := GoName(input); actual != expected {
			t.Errorf("GoName(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestGenerateRefusesAnInvalidDocument(t *testing.T) {
	if _, generateError := Generate([]byte("openapi: 3.1.0\ninfo: {}\npaths: {}\n"), "ankraapi"); generateError == nil {
		t.Fatal("a document without a title and version must not generate")
	}
}

func TestSchemaNamesThatCollideWithTheTransportGetASuffix(t *testing.T) {
	if name := schemaTypeName("Error"); name != "ErrorSchema" {
		t.Fatalf("schemaTypeName(Error) = %s", name)
	}
	if name := schemaTypeName("Server"); name != "Server" {
		t.Fatalf("schemaTypeName(Server) = %s", name)
	}
}
