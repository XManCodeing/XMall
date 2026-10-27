package httpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestFrontendPayloadRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("frontend contract test requires Node.js")
	}
	if _, err := os.Stat("../../../frontend/admin/node_modules/typescript/lib/typescript.js"); err != nil {
		t.Skip("frontend contract test requires installed frontend TypeScript")
	}
	frontend := func(input []byte) []byte {
		t.Helper()
		cmd := exec.Command("node", "frontend_payload.cjs")
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("frontend serializer: %v: %s", err, out)
		}
		return out
	}
	payload := frontend(nil)
	var input struct {
		Config Config `json:"config"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		t.Fatal(err)
	}
	if err := input.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var loaded struct {
		Config Config `json:"config"`
	}
	if err := json.Unmarshal(stored, &loaded); err != nil {
		t.Fatal(err)
	}
	// Feed backend serialization back through endpointDraft + serializeEndpoint.
	var before, after any
	if err := json.Unmarshal(payload, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(frontend(stored), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("frontend/backend round trip changed payload:\n%s\n%s", payload, stored)
	}
	ep := loaded.Config.Endpoints["ping"]
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/ping" || req.ContentLength != 0 || req.Header.Get("Content-Type") != "" {
			t.Fatalf("unexpected request: %#v", req)
		}
		return response(200, `{"ok":false,"id":9007199254740993}`), nil
	})
	result, err := executeWithTransport(context.Background(), "https://example.com", ep, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"zero": json.Number("0"), "false": false, "empty": "", "null": nil, "defaultZero": json.Number("0"), "defaultFalse": false, "defaultEmpty": "", "defaultNull": nil, "id": "9007199254740993"}
	if !reflect.DeepEqual(result.Mapped, want) {
		t.Fatalf("mapped: %#v", result.Mapped)
	}
}

func TestScalarConditionsAndPresence(t *testing.T) {
	for _, tt := range []struct {
		equals, response string
		pass             bool
	}{
		{`false`, `false`, true}, {`false`, `"false"`, false}, {`false`, `0`, false},
		{`0`, `0`, true}, {`0`, `0.0`, true}, {`0`, `"0"`, false}, {`0`, `false`, false},
		{`9007199254740993`, `9007199254740993`, true}, {`9007199254740993`, `9007199254740992`, false},
		{`""`, `""`, true}, {`""`, `null`, false}, {`null`, `null`, true},
		{`"true"`, `true`, true}, {`"0"`, `0`, true}, // legacy scalar text comparison
		{`true`, `{}`, false}, {`0`, `[]`, false},
	} {
		t.Run(tt.equals+"/"+tt.response, func(t *testing.T) {
			var ep Endpoint
			if err := json.Unmarshal([]byte(`{"success":{"path":"ok","equals":`+tt.equals+`}}`), &ep); err != nil {
				t.Fatal(err)
			}
			_, err := MapResponse(ep, []byte(`{"ok":`+tt.response+`}`))
			if (err == nil) != tt.pass {
				t.Fatalf("pass=%v, err=%v", tt.pass, err)
			}
			if _, err := MapResponse(ep, []byte(`{}`)); err == nil {
				t.Fatal("missing condition matched")
			}
		})
	}
	for _, raw := range []string{
		`{"success":{"path":"ok"}}`,
		`{"success":{"equals":false}}`,
		`{"fields":{"x":{"path":"x","value":0}}}`,
		`{"fields":{"x":{"path":"x","value":false}}}`,
		`{"fields":{"x":{"path":"x","value":null}}}`,
		`{"fields":{"x":{"required":true}}}`,
	} {
		var ep Endpoint
		if err := json.Unmarshal([]byte(raw), &ep); err != nil {
			t.Fatal(err)
		}
		if _, err := MapResponse(ep, []byte(`{}`)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestTypedConstantsDefaultsAndRedaction(t *testing.T) {
	var ep Endpoint
	raw := `{"path":"/ping","encoding":"none","fields":{"zero":{"value":0},"false":{"value":false},"empty":{"value":""},"null":{"value":null},"absent":{},"fallback":{"path":"missing","default":false},"source":{"path":"source","default":9},"int":{"value":0,"transform":"int"},"bool":{"default":false,"transform":"bool"}}}`
	if err := json.Unmarshal([]byte(raw), &ep); err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &ep); err != nil {
		t.Fatal(err)
	}
	mapped, err := MapResponse(ep, []byte(`{"source":0}`))
	want := map[string]any{"zero": json.Number("0"), "false": false, "empty": "", "null": nil, "fallback": false, "source": json.Number("0"), "int": int64(0), "bool": false}
	if err != nil || !reflect.DeepEqual(mapped, want) {
		t.Fatalf("mapped=%#v err=%v", mapped, err)
	}
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"source":0}`), nil })
	result, err := executeWithTransport(context.Background(), "https://example.com", ep, map[string]string{"token": "0"}, tr)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mapped["zero"] != "[REDACTED]" || result.Mapped["int"] != "[REDACTED]" || strings.Contains(result.Body, ":0") {
		t.Fatalf("typed secret leaked: %#v", result)
	}
}

func TestScalarRejectsCompositeAndExcessiveNumbers(t *testing.T) {
	for _, constant := range []string{`{}`, `[]`, `{"x":true}`, `1e1001`, `1e-1001`, strings.Repeat("9", 257)} {
		for _, raw := range []string{
			`{"success":{"path":"ok","equals":` + constant + `}}`,
			`{"fields":{"x":{"value":` + constant + `}}}`,
			`{"fields":{"x":{"default":` + constant + `}}}`,
		} {
			var ep Endpoint
			if err := json.Unmarshal([]byte(raw), &ep); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		}
	}
}

func TestLegacyStringConfiguration(t *testing.T) {
	var cfg Config
	// This is the old Go encoder's shape, including empty placeholders.
	raw := `{"endpoints":{"ping":{"path":"/ping","encoding":"","success":{"path":"","equals":""},"fields":{"id":{"path":"id","value":"","default":"","required":true},"constant":{"path":"","value":"fixed","default":""}}}}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ep := cfg.Endpoints["ping"]
	if _, err := MapResponse(ep, []byte(`{}`)); err == nil {
		t.Fatal("legacy empty default bypassed required path")
	}
	// Existing callers can still normalize strings in place.
	f := ep.Fields["constant"]
	f.Value = "normalized"
	ep.Fields["constant"] = f
	stored, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &ep); err != nil {
		t.Fatal(err)
	}
	mapped, err := MapResponse(ep, []byte(`{"id":"42"}`))
	if err != nil || mapped["id"] != "42" || mapped["constant"] != "normalized" {
		t.Fatalf("%#v %v", mapped, err)
	}
}

func TestNoneEncodingAndCurlRoundTrip(t *testing.T) {
	for _, encoding := range []string{"", "none", "xml"} {
		ep := Endpoint{Path: "/", Encoding: encoding, Body: map[string]string{"x": ""}}
		if err := validateEndpoint(ep); err == nil {
			t.Fatalf("accepted body with %q", encoding)
		}
		if _, err := buildRequest(context.Background(), "https://example.com", ep, nil); err == nil {
			t.Fatal("buildRequest dropped body")
		}
		tr := roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("transport called"); return nil, nil })
		if _, err := executeWithTransport(context.Background(), "https://example.com", ep, nil, tr); err == nil {
			t.Fatal("execute dropped body")
		}
	}
	for _, tt := range []struct{ curl, encoding, body string }{
		{`curl https://example.com/ping`, "none", ""},
		{`curl https://example.com/ping -H 'Content-Type: application/json' -d '{"x":"0"}'`, "json", `{"x":"0"}`},
		{`curl https://example.com/ping -d 'x=false'`, "form", "x=false"},
	} {
		spec, err := ParseCurl(tt.curl)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Encoding != tt.encoding {
			t.Fatalf("encoding %q", spec.Encoding)
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var ep Endpoint
		if err := json.Unmarshal(raw, &ep); err != nil {
			t.Fatal(err)
		}
		tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body []byte
			if req.Body != nil {
				body, err = io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if string(body) != tt.body {
				t.Fatalf("body %q", body)
			}
			return response(200, `{}`), nil
		})
		if _, err := executeWithTransport(context.Background(), "https://example.com", ep, nil, tr); err != nil {
			t.Fatal(err)
		}
	}
}
