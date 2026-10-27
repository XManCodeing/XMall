package httpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestMappingExactAndList(t *testing.T) {
	ep := Endpoint{Success: Condition{Path: "ok", Equals: "true"}, ListPath: "data", Fields: map[string]Field{
		"id": {Path: "id", Transform: "int", Required: true}, "amount": {Path: "cents", Transform: "cents_to_units"},
		"decimal": {Path: "price", Transform: "decimal"}, "state": {Path: "state", Enum: map[string]string{"1": "ready"}},
		"static": {Value: "fixed"}, "fallback": {Path: "missing", Default: "x"}, "nested": {Path: "nested.0.x"}, "absent": {Path: "absent"},
	}}
	got, err := MapResponse(ep, []byte(`{"ok":true,"data":[{"id":9007199254740993,"cents":"900719925474099399","price":0.1234567890123456789,"state":1,"nested":[{"x":"yes"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	row := got["items"].([]any)[0].(map[string]any)
	if row["id"] != int64(9007199254740993) || row["amount"].(decimal.Decimal).String() != "9007199254740993.99" || row["decimal"].(decimal.Decimal).String() != "0.1234567890123456789" {
		t.Fatalf("precision lost: %#v", row)
	}
	if row["state"] != "ready" || row["static"] != "fixed" || row["fallback"] != "x" || row["nested"] != "yes" {
		t.Fatalf("bad mapping: %#v", row)
	}
	if _, ok := row["absent"]; ok {
		t.Fatal("optional missing field should be omitted")
	}
}
func TestMappingRejectsAmbiguityAndLimits(t *testing.T) {
	tests := []struct {
		name string
		ep   Endpoint
		raw  string
	}{
		{"missing required", Endpoint{Fields: map[string]Field{"x": {Path: "x", Required: true}}}, `{}`},
		{"unknown enum", Endpoint{Fields: map[string]Field{"x": {Path: "x", Enum: map[string]string{"ok": "ready"}}}}, `{"x":"unknown"}`},
		{"bad decimal", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "decimal"}}}, `{"x":"not money"}`},
		{"huge exponent", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "decimal"}}}, `{"x":"1e999999999"}`},
		{"fractional cents", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "cents_to_units"}}}, `{"x":1.5}`},
		{"guess bool", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "bool"}}}, `{"x":1}`},
		{"fractional int", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "int"}}}, `{"x":1.5}`},
		{"overflow int", Endpoint{Fields: map[string]Field{"x": {Path: "x", Transform: "int"}}}, `{"x":999999999999999999999}`},
		{"bad success", Endpoint{Success: Condition{Path: "code", Equals: "0"}}, `{"code":1}`},
		{"success lacks path", Endpoint{Success: Condition{Equals: "0"}}, `{}`},
		{"success lacks equals", Endpoint{Success: Condition{Path: "code"}}, `{}`},
		{"list missing", Endpoint{ListPath: "items"}, `{}`},
		{"list scalar", Endpoint{ListPath: "items"}, `{"items":1}`},
		{"duplicate keys", Endpoint{}, `{"x":1,"x":2}`},
		{"trailing JSON", Endpoint{}, `{} {}`},
		{"deep JSON", Endpoint{}, strings.Repeat("[", maxDepth+2) + "0" + strings.Repeat("]", maxDepth+2)},
		{"big JSON", Endpoint{}, strings.Repeat(" ", maxBytes+1)},
		{"eval path", Endpoint{Fields: map[string]Field{"x": {Path: "a[0]"}}}, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := MapResponse(tt.ep, []byte(tt.raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
func TestConfigValidate(t *testing.T) {
	good := Config{Endpoints: map[string]Endpoint{"products": {Path: "/products", Method: "GET"}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []Endpoint{{Path: "/", Encoding: "xml"}, {Path: "/", Method: "CONNECT"}, {Path: "/", Body: map[string]string{"x": "y"}}, {Path: "/", Headers: map[string]string{"Host": "example.com"}}, {Path: "/", Headers: map[string]string{"X-A": "x", "x-a": "y"}}} {
		if err := (Config{Endpoints: map[string]Endpoint{"x": ep}}).Validate(); err == nil {
			t.Fatalf("accepted %#v", ep)
		}
	}
}
func TestParseCurl(t *testing.T) {
	s, err := ParseCurl(`curl -X POST 'https://example.com/api?q=a%20b' -H 'Content-Type: application/json' -H 'Authorization: Bearer test' --data-raw '{"name":"hello","quote":"a\"b"}'`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != "https://example.com/api?q=a%20b" || s.Encoding != "json" || s.Body["quote"] != "a\"b" || s.Method != "POST" {
		t.Fatalf("bad curl: %#v", s)
	}
	s, err = ParseCurl("curl \\\n --url https://example.com -b 'a=b' --data-urlencode 'a=hello +世界' --data-urlencode 'b=x=y'")
	if err != nil {
		t.Fatal(err)
	}
	if s.Body["a"] != "hello +世界" || s.Body["b"] != "x=y" || s.Headers["Cookie"] != "a=b" {
		t.Fatalf("bad form: %#v", s)
	}
	s, err = ParseCurl(`curl https://example.com -d 'a=hello%20world&b=x%2By'`)
	if err != nil || s.Body["a"] != "hello world" || s.Body["b"] != "x+y" {
		t.Fatalf("bad form: %#v %v", s, err)
	}
	s, err = ParseCurl(`curl "https://example.com" -H "X-Test: escaped\"quote"`)
	if err != nil || s.Headers["X-Test"] != "escaped\"quote" {
		t.Fatalf("bad double quotes: %#v %v", s, err)
	}
}
func TestParseCurlRejects(t *testing.T) {
	for _, input := range []string{
		`curl https://example.com -L`, `curl https://example.com --insecure`, `curl https://example.com --proxy x`,
		`curl https://example.com -d @file`, `curl https://example.com --data-raw @file`, `curl https://example.com -b cookies.txt`,
		`curl https://example.com -H @headers`, `curl https://example.com --data-urlencode a@file`,
		`curl https://example.com; touch x`, `curl https://example.com && echo x`, `curl https://example.com > file`,
		"curl https://example.com\necho x", `curl 'https://example.com/$(id)'`, "curl 'https://example.com/`id`'",
		`curl https://example.com https://other.com`, `curl 'https://example.com?a=1&a=2'`,
		`curl https://example.com -H 'X-A: 1' -H 'x-a: 2'`, `curl https://example.com -d a=1 -d b=2`,
		`curl https://example.com -d 'a=1&a=2'`, `curl https://example.com --data-urlencode a=1 --data-urlencode a=2`,
		`curl https://example.com -H 'Content-Type: application/json' -d '{"n":"a","n":"b"}'`,
		`curl 'https://example.com`, `curl https://example.com -X`, `curl ftp://example.com`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseCurl(input); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestExecuteTemplatesAndRedaction(t *testing.T) {
	ep := Endpoint{Method: "POST", Path: "/items/{{id}}", Encoding: "json", Query: map[string]string{"q": "{{query}}"}, Body: map[string]string{"value": "{{value}}"}, Headers: map[string]string{"Authorization": "Bearer fixed-secret", "Cookie": "session=fixed-cookie", "X-Token": "{{token}}"}, Fields: map[string]Field{"value": {Path: "value"}, "secret": {Path: "secret"}}}
	vars := map[string]string{"id": "a/b c", "query": "a&b=+中", "value": "123", "token": "private-token", "credential_password": "hidden-password"}
	called := 0
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called++
		if r.URL.EscapedPath() != "/items/a%2Fb%20c" || r.URL.Query().Get("q") != vars["query"] {
			t.Fatalf("bad URL: %s", r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"value":"123"}` {
			t.Fatalf("body coerced: %s", b)
		}
		if r.Header.Get("Authorization") != "Bearer fixed-secret" || r.Header.Get("X-Token") != "private-token" {
			t.Fatal("request secrets altered")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 15*time.Second {
			t.Fatal("missing timeout")
		}
		if r.GetBody != nil {
			t.Fatal("request is replayable")
		}
		return response(200, `{"value":"ok","secret":"private-token fixed-secret fixed-cookie hidden-password"}`), nil
	})
	result, err := executeWithTransport(context.Background(), "https://example.com", ep, vars, tr)
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || result.StatusCode != 200 || result.Mapped["value"] != "ok" {
		t.Fatalf("bad result: %#v", result)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-token", "fixed-secret", "fixed-cookie", "hidden-password"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s: %s", secret, raw)
		}
	}
}
func TestExecuteRejectsTargetsAndHeaders(t *testing.T) {
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("transport should not run"); return nil, nil })
	tests := []struct {
		base string
		ep   Endpoint
		vars map[string]string
	}{
		{"http://127.0.0.1", Endpoint{Path: "/"}, nil},
		{"http://169.254.169.254", Endpoint{Path: "/"}, nil},
		{"http://[::1]", Endpoint{Path: "/"}, nil},
		{"https://user:pass@example.com", Endpoint{Path: "/"}, nil},
		{"https://example.com?q=x", Endpoint{Path: "/"}, nil},
		{"https://example.com#x", Endpoint{Path: "/"}, nil},
		{"ftp://example.com", Endpoint{Path: "/"}, nil},
		{"https://example.com", Endpoint{Path: "https://other.com/"}, nil},
		{"https://example.com", Endpoint{Path: "http://example.com/"}, nil},
		{"https://example.com", Endpoint{Path: "//other.com/"}, nil},
		{"https://example.com", Endpoint{Path: "/{{missing}}"}, nil},
		{"https://example.com", Endpoint{Path: "/?x={{token}}"}, map[string]string{"token": "x"}},
		{"https://example.com", Endpoint{Path: "/", Headers: map[string]string{"Content-Length": "3"}}, nil},
		{"https://example.com", Endpoint{Path: "/", Headers: map[string]string{"X-Test": "{{x}}"}}, map[string]string{"x": "a\r\nb"}},
		{"https://example.com", Endpoint{Path: "/?q=a", Query: map[string]string{"q": "b"}}, nil},
		{"https://example.com", Endpoint{Path: "/", Encoding: "json", Headers: map[string]string{"Content-Type": "text/plain"}}, nil},
	}
	for _, tt := range tests {
		if _, err := executeWithTransport(context.Background(), tt.base, tt.ep, tt.vars, tr); err == nil {
			t.Fatalf("accepted: %#v", tt)
		}
	}
}
func TestExecuteResponseFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"redirect", 302, `{"token":"sensitive"}`}, {"http error", 500, `{"error":"sensitive"}`},
		{"oversized", 200, strings.Repeat("x", maxBytes+1)}, {"malformed", 200, "not json sensitive"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(tt.status, tt.body)
				r.Header.Set("Location", "http://127.0.0.1")
				return r, nil
			})
			r, err := executeWithTransport(context.Background(), "https://example.com", Endpoint{Path: "/"}, map[string]string{"token": "sensitive"}, tr)
			if err == nil || calls != 1 {
				t.Fatalf("want error, no retry: %v %d", err, calls)
			}
			if r != nil && strings.Contains(r.Body, "sensitive") {
				t.Fatal("response secret leaked")
			}
		})
	}
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret diagnostic") })
	_, err := executeWithTransport(context.Background(), "https://example.com", Endpoint{Path: "/"}, nil, tr)
	if err == nil || strings.Contains(err.Error(), "secret diagnostic") {
		t.Fatal("transport diagnostic leaked")
	}
}
func TestExecuteFormAndCancellation(t *testing.T) {
	ep := Endpoint{Method: "POST", Path: "/submit", Encoding: "form", Body: map[string]string{"v": "{{value}}"}}
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "v=a%26b%3D%2B%E4%B8%AD" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("invalid form: %s", b)
		}
		return response(200, " {\n \"ok\": true\n } "), nil
	})
	r, err := executeWithTransport(context.Background(), "https://example.com", ep, map[string]string{"value": "a&b=+中"}, tr)
	if err != nil || r.Body != " {\n \"ok\": true\n } " {
		t.Fatalf("unaltered raw response was not preserved: %#v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() == nil {
			t.Fatal("cancellation was not propagated")
		}
		return nil, r.Context().Err()
	})
	if _, err := executeWithTransport(ctx, "https://example.com", Endpoint{Path: "/"}, nil, tr); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestPublicIP(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "::1", "::", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:7f00::1", "168.63.129.16", "224.0.0.1", "198.18.0.1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("rejected %s", ip)
		}
	}
	if _, err := safeDial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dial permitted loopback")
	}
}
func TestRedactEscapedSecrets(t *testing.T) {
	redact := redactor(Endpoint{}, map[string]string{"api_key": "a&b", "token": "secret"})
	body := redactBody(`{"x":"\u0073ecret","url":"a%26b"}`, redact)
	if strings.Contains(body, "secret") || strings.Contains(body, `\u0073ecret`) || strings.Contains(body, "a%26b") {
		t.Fatalf("leaked: %s", body)
	}
}
