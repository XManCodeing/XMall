package httpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLegacySerializedConfigCompatibility(t *testing.T) {
	raw := `{"endpoints":{"create":{"method":"POST","path":"/create","encoding":"json","headers":{},"query":{},"body":{"count":"{{count}}"},"success":{"path":"code","equals":"0"},"fields":{"id":{"path":"data.id","value":"","default":"","required":true,"transform":"","enum":null}},"list_path":""}},"credentials":{"key":"legacy"}}`
	var config Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	ep := config.Endpoints["create"]
	req, err := buildRequest(context.Background(), "https://example.com", ep, map[string]string{"count": "2"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"count":"2"}` {
		t.Fatalf("legacy body changed: %s", body)
	}
	mapped, err := MapResponse(ep, []byte(`{"code":0,"data":{"id":"order"}}`))
	if err != nil || mapped["id"] != "order" {
		t.Fatalf("legacy mapping: %v %v", mapped, err)
	}
	encoded, err := json.Marshal(config)
	if err != nil || strings.Contains(string(encoded), `"version"`) || strings.Contains(string(encoded), `"json_body"`) {
		t.Fatalf("legacy serialization changed: %s %v", encoded, err)
	}
	config.Version = 1
	encoded, _ = json.Marshal(config)
	if !strings.Contains(string(encoded), `"version":1`) {
		t.Fatal("version missing")
	}
}

func TestTypedJSONTemplatesAndCurl(t *testing.T) {
	raw := `{"count":9007199254740993,"flag":true,"nil":null,"nested":[{"quantity":"{{quantity}}","object":"{{manual_form_data}}","text":"id={{quantity}}","string":"{{string}}"}]}`
	ep := Endpoint{Path: "/create", Method: "POST", Encoding: "json", JSONBody: json.RawMessage(raw)}
	vars := map[string]string{"quantity": "2", "manual_form_data": `{"ok":false,"items":[null,1]}`, "string": "123"}
	req, err := buildRequest(context.Background(), "https://example.com", ep, vars)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	value, err := decodeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	obj := value.(map[string]any)
	row := obj["nested"].([]any)[0].(map[string]any)
	if obj["count"] != json.Number("9007199254740993") || obj["flag"] != true || obj["nil"] != nil || row["quantity"] != json.Number("2") || row["text"] != "id=2" || row["string"] != "123" {
		t.Fatalf("types lost: %s", body)
	}
	if row["object"].(map[string]any)["ok"] != false {
		t.Fatal("object lost")
	}
	spec, err := ParseCurl(`curl https://example.com/create -H 'Content-Type: application/json' -d '` + raw + `'`)
	if err != nil || string(spec.JSONBody) != raw || len(spec.Body) != 0 {
		t.Fatalf("typed import: %#v %v", spec, err)
	}
	for _, raw := range []string{`[1,true,null]`, `null`, `123`} {
		spec, err := ParseCurl(`curl https://example.com -H 'Content-Type: application/json' -d '` + raw + `'`)
		if err != nil || string(spec.JSONBody) != raw {
			t.Fatalf("root JSON import %s: %v", raw, err)
		}
	}
	ep.Body = map[string]string{"x": "y"}
	if _, err := buildRequest(context.Background(), "https://example.com", ep, vars); err == nil {
		t.Fatal("ambiguous bodies accepted")
	}
}

func TestListMetadataCollectionsAndBudgets(t *testing.T) {
	ep := Endpoint{List: true, Fields: map[string]Field{"id": {Path: "id"}}, Collections: map[string]Collection{"skus": {Path: "variants", Fields: map[string]Field{"id": {Path: "id", Required: true}, "stock": {Path: "stock", Transform: "int"}}}}}
	raw := `[{"id":1,"variants":[{"id":"sku","stock":0}]}]`
	mapped, err := MapResponse(ep, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	item := mapped["items"].([]any)[0].(map[string]any)
	sku := item["skus"].([]any)[0].(map[string]any)
	if sku["id"] != "sku" || sku["stock"] != int64(0) {
		t.Fatal(mapped)
	}
	ep.ListPath = "data"
	ep.MetaFields = map[string]Field{"total": {Path: "total", Transform: "int"}}
	mapped, err = MapResponse(ep, []byte(`{"total":42,"data":`+raw+`}`))
	if err != nil || mapped["total"] != int64(42) {
		t.Fatalf("metadata: %v %v", mapped, err)
	}
	ep.MetaFields["items"] = Field{Path: "total"}
	if _, err := MapResponse(ep, []byte(`{}`)); err == nil {
		t.Fatal("items overwrite allowed")
	}
	ep = Endpoint{Fields: map[string]Field{"one": {Path: "data"}, "two": {Path: "data"}}}
	big := `{"data":[` + strings.Repeat(`0,`, 6000) + `0]}`
	if _, err := MapResponse(ep, []byte(big)); err == nil {
		t.Fatal("repeated subtree node budget bypass")
	}
	big = `{"data":"` + strings.Repeat("x", maxBytes/2) + `"}`
	if _, err := MapResponse(ep, []byte(big)); err == nil {
		t.Fatal("repeated subtree byte budget bypass")
	}
	deep := strings.Repeat("[", maxDepth-1) + "0" + strings.Repeat("]", maxDepth-1)
	ep = Endpoint{List: true, Fields: map[string]Field{"copy": {Path: "v"}}}
	if _, err := MapResponse(ep, []byte(`[{"v":`+deep+`}]`)); err == nil {
		t.Fatal("expanded depth budget bypass")
	}
}

func TestExecutionModesAndErrorClassification(t *testing.T) {
	ep := Endpoint{Path: "/", Fields: map[string]Field{"payload": {Path: "payload"}}}
	vars := map[string]string{"token": "business-secret"}
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"payload":"business-secret"}`), nil
	})
	for _, business := range []bool{true, false} {
		result, err := executeModeWithTransport(context.Background(), "https://example.com", ep, vars, tr, business)
		if err != nil {
			t.Fatal(err)
		}
		if business && (result.Mapped["payload"] != "business-secret" || !strings.Contains(result.Body, "business-secret")) {
			t.Fatal("business data redacted")
		}
		if !business && (strings.Contains(result.Body, "business-secret") || result.Mapped["payload"] == "business-secret") {
			t.Fatal("diagnostic leaked")
		}
	}
	cases := []struct {
		name string
		ep   Endpoint
		tr   roundTripFunc
		want ExecutionErrorKind
	}{
		{"validation", Endpoint{}, tr, NotSent},
		{"transport", ep, func(*http.Request) (*http.Response, error) { return nil, errors.New("secret URL") }, Unknown},
		{"HTTP rejection", ep, func(*http.Request) (*http.Response, error) {
			return response(400, `{"payload":"business-secret"}`), nil
		}, Unknown},
		{"HTTP 500", ep, func(*http.Request) (*http.Response, error) {
			return response(500, `{"payload":"business-secret"}`), nil
		}, Unknown},
		{"success mismatch", Endpoint{Path: "/", Success: Condition{Path: "ok", Equals: "true"}}, func(*http.Request) (*http.Response, error) { return response(200, `{"ok":false}`), nil }, Unknown},
		{"mapping", ep, func(*http.Request) (*http.Response, error) { return response(200, `malformed business-secret`), nil }, Unknown},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executeWithTransport(context.Background(), "https://example.com", tt.ep, vars, tt.tr)
			var execution *ExecutionError
			if !errors.As(err, &execution) || execution.Kind != tt.want {
				t.Fatalf("classification: %v", err)
			}
			if strings.Contains(err.Error(), "business-secret") || strings.Contains(err.Error(), "secret URL") {
				t.Fatal("unsafe error")
			}
			if result != nil && (result.Error == "" || strings.Contains(result.Body, "business-secret") || result.StatusCode == 0) {
				t.Fatalf("bad debug failure result: %#v", result)
			}
		})
	}
	for _, execute := range []func(context.Context, string, Endpoint, map[string]string) (*Result, error){Execute, ExecuteBusiness} {
		_, err := execute(context.Background(), "http://127.0.0.1", ep, nil)
		var execution *ExecutionError
		if !errors.As(err, &execution) || execution.Kind != NotSent {
			t.Fatalf("SSRF classification: %v", err)
		}
	}
}

func TestTypedExpansionBudgetsAndScalars(t *testing.T) {
	for _, raw := range []string{"true", "false", "null", "9007199254740993", `{"a":[1,false,null]}`} {
		ep := Endpoint{Path: "/", Encoding: "json", JSONBody: json.RawMessage(`"{{manual_form_data}}"`)}
		req, err := buildRequest(context.Background(), "https://example.com", ep, map[string]string{"manual_form_data": raw})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(req.Body)
		if string(body) != raw {
			t.Fatalf("typed variable changed: %s != %s", body, raw)
		}
	}
	root, err := decodeJSON([]byte(`["{{manual_form_data}}","{{manual_form_data}}"]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		`[` + strings.Repeat("0,", 6000) + `0]`,
		`"` + strings.Repeat("x", maxBytes/2) + `"`,
		strings.Repeat("[", maxDepth) + "0" + strings.Repeat("]", maxDepth),
	} {
		if _, err := expandJSON(root, map[string]string{"manual_form_data": value}, &outputBudget{}, 0); err == nil {
			t.Fatal("expanded template budget bypass")
		}
	}
}

func TestOpaqueJSONPlaceholders(t *testing.T) {
	for _, name := range []string{"product_id", "order_id", "credential_token", "api_key", "object", "value", "Quantity", "credential_quantity"} {
		for _, raw := range []string{"true", "null", "123", "001", "false", `{"a":[1,null]}`, `"123"`, ""} {
			t.Run(name+"/"+raw, func(t *testing.T) {
				ep := Endpoint{Path: "/", Encoding: "json", JSONBody: json.RawMessage(`{"value":"{{` + name + `}}"}`)}
				req, err := buildRequest(context.Background(), "https://example.com", ep, map[string]string{name: raw})
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(req.Body)
				value, err := decodeJSON(body)
				if err != nil || value.(map[string]any)["value"] != raw {
					t.Fatalf("opaque value changed: %s (%v)", body, err)
				}
			})
		}
	}
	// Opaque expansion still consumes the total byte budget.
	if _, err := expandJSON([]any{"{{product_id}}", "{{product_id}}"}, map[string]string{"product_id": strings.Repeat("x", maxBytes/2)}, &outputBudget{}, 0); err == nil {
		t.Fatal("opaque byte budget bypass")
	}
}

func TestSystemVariableTypingOnlyInJSONBody(t *testing.T) {
	vars := map[string]string{"quantity": "2", "page": "1", "page_size": "100", "offset": "0", "include_inactive": "false", "manual_form_data": `{"options":[true,null,123]}`, "product_id": "true", "order_id": "null", "credential_token": "123", "sku_id": "001"}
	templates := map[string]string{}
	for name := range vars {
		templates[name] = "{{" + name + "}}"
	}
	raw, _ := json.Marshal(templates)
	ep := Endpoint{Path: "/", Encoding: "json", JSONBody: raw, Query: templates}
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		for name, value := range vars {
			if req.URL.Query().Get(name) != value {
				t.Fatalf("query changed %s", name)
			}
		}
		body, _ := io.ReadAll(req.Body)
		decoded, err := decodeJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		values := decoded.(map[string]any)
		for _, name := range []string{"quantity", "page", "page_size", "offset"} {
			if values[name] != json.Number(vars[name]) {
				t.Fatalf("system number not typed: %s", name)
			}
		}
		if values["include_inactive"] != false {
			t.Fatal("system boolean not typed")
		}
		if _, ok := values["manual_form_data"].(map[string]any); !ok {
			t.Fatal("manual form object not typed")
		}
		for _, name := range []string{"product_id", "order_id", "credential_token", "sku_id"} {
			if values[name] != vars[name] {
				t.Fatalf("opaque ID changed: %s", name)
			}
		}
		return response(200, `{}`), nil
	})
	if _, err := executeModeWithTransport(context.Background(), "https://example.com", ep, vars, tr, true); err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []string{"json", "form"} {
		ep.Encoding, ep.JSONBody, ep.Body = encoding, nil, templates
		tr = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			for name, value := range vars {
				if req.URL.Query().Get(name) != value {
					t.Fatalf("query changed %s", name)
				}
			}
			body, _ := io.ReadAll(req.Body)
			var values map[string]string
			if encoding == "json" {
				if err := json.Unmarshal(body, &values); err != nil {
					t.Fatal(err)
				}
			} else {
				form, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatal(err)
				}
				values = map[string]string{}
				for name := range vars {
					values[name] = form.Get(name)
				}
			}
			for name, value := range vars {
				if values[name] != value {
					t.Fatalf("legacy/form value changed: %s", name)
				}
			}
			return response(200, `{}`), nil
		})
		if _, err := executeModeWithTransport(context.Background(), "https://example.com", ep, vars, tr, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"quantity", "page", "page_size", "offset", "include_inactive", "manual_form_data"} {
		for _, raw := range []string{"001", "not JSON"} {
			value, err := expandJSON("{{"+name+"}}", map[string]string{name: raw}, &outputBudget{}, 0)
			if err != nil || value != raw {
				t.Fatalf("invalid JSON fallback changed: %s %v", name, err)
			}
		}
		value, err := expandJSON("prefix={{"+name+"}}", vars, &outputBudget{}, 0)
		if err != nil || value != "prefix="+vars[name] {
			t.Fatalf("embedded placeholder changed: %s %v", name, err)
		}
	}
}

func TestNormalizeCurlBasePathRoundTrip(t *testing.T) {
	for _, base := range []string{"https://example.com", "https://example.com/api", "https://example.com/api/"} {
		spec, err := ParseCurl(`curl 'https://example.com/api/products?q=a%2Bb' -H 'Content-Type: application/json' -d '{"n":1}'`)
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := NormalizeCurl(base, spec)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(normalized.Path, "http") || strings.Contains(normalized.Path, "?") || normalized.Query["q"] != "a+b" || string(normalized.JSONBody) != `{"n":1}` {
			t.Fatalf("bad normalize: %#v", normalized)
		}
		req, err := buildRequest(context.Background(), base, Endpoint{Path: normalized.Path, Encoding: normalized.Encoding, JSONBody: normalized.JSONBody, Query: normalized.Query, Headers: normalized.Headers}, nil)
		if err != nil || req.URL.String() != "https://example.com/api/products?q=a%2Bb" {
			t.Fatalf("base path lost: %v %v", req, err)
		}
	}
	for _, target := range []string{"https://other.com/api/x", "http://example.com/api/x", "https://example.com/api2/x", "https://example.com/outside", "https://example.com/api/%2e%2e/private", "https://example.com/api/../private"} {
		if _, err := NormalizeCurl("https://example.com/api/", RequestSpec{Path: target}); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	if _, err := NormalizeCurl("https://example.com/api/", RequestSpec{Path: "https://example.com/api/x?q=1", Query: map[string]string{"q": "2"}}); err == nil {
		t.Fatal("duplicate query accepted")
	}
}
