package cloudapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ankraio/ankra-cloud-ccm/internal/ankraapi"
)

// The documents this package decodes and the bodies it sends by hand must agree with the OpenAPI schemas they stand
// for. The generated ankraapi types are those schemas (make client-check keeps them equal to api/openapi.yaml, and
// make sync-client keeps api/openapi.yaml equal to the Ankra Cloud document), so every JSON field written here has
// to exist there with the same JSON type.
func TestHandWrittenDocumentsMatchTheOpenAPISchemas(t *testing.T) {
	for _, pair := range []struct {
		name      string
		local     any
		generated any
	}{
		{name: "ZoneCapabilities", local: zoneCapabilitiesDocument{}, generated: ankraapi.ZoneCapabilities{}},
		{name: "LoadBalancer", local: loadBalancerDocument{}, generated: ankraapi.LoadBalancer{}},
		{name: "CreateLoadBalancerRequest", local: createLoadBalancerBody{}, generated: ankraapi.CreateLoadBalancerRequest{}},
		{name: "UpdateLoadBalancerRequest", local: updateLoadBalancerBody{}, generated: ankraapi.UpdateLoadBalancerRequest{}},
		{name: "ReplaceLoadBalancerMembersRequest", local: replaceMembersBody{}, generated: ankraapi.ReplaceLoadBalancerMembersRequest{}},
	} {
		compareJSONShapes(t, pair.name, reflect.TypeOf(pair.local), reflect.TypeOf(pair.generated))
	}
}

// The real answers decode into the generated schema type with no field left over, so the testdata is the current
// API's shape and not a hand-written guess.
func TestZoneCapabilitiesTestdataIsTheSchema(t *testing.T) {
	for _, name := range []string{"zone-capabilities-de-fsn1.json", "zone-capabilities-de-fsn2.json"} {
		decoder := json.NewDecoder(bytes.NewReader([]byte(readTestdata(t, name))))
		decoder.DisallowUnknownFields()
		var capabilities ankraapi.ZoneCapabilities
		if decodeError := decoder.Decode(&capabilities); decodeError != nil {
			t.Fatalf("%s: %v", name, decodeError)
		}
	}
}

type jsonKind int

const (
	jsonKindString jsonKind = iota
	jsonKindNumber
	jsonKindBoolean
	jsonKindArray
	jsonKindObject
	jsonKindAny
)

func kindOf(goType reflect.Type) jsonKind {
	switch goType.Kind() {
	case reflect.String:
		return jsonKindString
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return jsonKindNumber
	case reflect.Bool:
		return jsonKindBoolean
	case reflect.Slice, reflect.Array:
		return jsonKindArray
	case reflect.Map, reflect.Struct:
		if goType.Kind() == reflect.Struct && goType.PkgPath() == "time" {
			return jsonKindString
		}
		return jsonKindObject
	default:
		return jsonKindAny
	}
}

func dereference(goType reflect.Type) reflect.Type {
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	return goType
}

func jsonFields(goType reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for index := range goType.NumField() {
		field := goType.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" || !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func compareJSONShapes(t *testing.T, path string, local reflect.Type, generated reflect.Type) {
	t.Helper()
	local, generated = dereference(local), dereference(generated)
	if local == generated {
		return
	}
	localKind, generatedKind := kindOf(local), kindOf(generated)
	if localKind == jsonKindAny || generatedKind == jsonKindAny {
		return
	}
	if localKind != generatedKind {
		t.Errorf("%s: decoded as %s here, the OpenAPI schema says %s", path, local, generated)
		return
	}
	switch local.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		compareJSONShapes(t, path+"[]", local.Elem(), generated.Elem())
	case reflect.Struct:
		if generated.Kind() != reflect.Struct {
			return
		}
		generatedFields := jsonFields(generated)
		for name, localType := range jsonFields(local) {
			generatedType, isDeclared := generatedFields[name]
			if !isDeclared {
				t.Errorf("%s.%s: not in the OpenAPI schema", path, name)
				continue
			}
			compareJSONShapes(t, path+"."+name, localType, generatedType)
		}
	}
}
