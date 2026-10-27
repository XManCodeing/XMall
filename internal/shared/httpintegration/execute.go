package httpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var placeholder = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// Execute performs one request with a 15-second maximum lifetime. The production
// transport always validates resolved addresses and dials the validated IP.
// Result is diagnostic: Body and Mapped are recursively redacted, including keys
// and numeric values. It must not be used as an unmodified fulfillment payload.
func Execute(ctx context.Context, baseURL string, ep Endpoint, variables map[string]string) (*Result, error) {
	return execute(ctx, baseURL, ep, variables, false)
}

// ExecuteBusiness returns unredacted fulfillment data. Never expose its Result
// through debug/admin APIs. Errors are always safe, and SSRF rules are identical
// to Execute. A failure after dispatch does not prove an order was not created.
func ExecuteBusiness(ctx context.Context, baseURL string, ep Endpoint, variables map[string]string) (*Result, error) {
	return execute(ctx, baseURL, ep, variables, true)
}
func execute(ctx context.Context, baseURL string, ep Endpoint, variables map[string]string, business bool) (*Result, error) {
	transport := &http.Transport{Proxy: nil, DialContext: safeDial, DisableKeepAlives: true, DisableCompression: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	defer transport.CloseIdleConnections()
	return executeModeWithTransport(ctx, baseURL, ep, variables, transport, business)
}

// The injection seam is deliberately unexported; callers cannot disable SSRF checks.
func executeWithTransport(ctx context.Context, baseURL string, ep Endpoint, vars map[string]string, transport http.RoundTripper) (*Result, error) {
	return executeModeWithTransport(ctx, baseURL, ep, vars, transport, false)
}
func executeModeWithTransport(ctx context.Context, baseURL string, ep Endpoint, vars map[string]string, transport http.RoundTripper, business bool) (result *Result, err error) {
	kind := NotSent
	redact := func(s string) string { return s }
	defer func() {
		if err != nil {
			err = &ExecutionError{Kind: kind, message: redact(err.Error())}
			if result != nil {
				result.Error = err.Error()
			}
		}
		if !business && result != nil {
			result.Body = redactBody(result.Body, redact)
			if result.Mapped != nil {
				result.Mapped = redactValue(result.Mapped, redact).(map[string]any)
			}
		}
	}()
	// Validate limits before constructing the redactor (which creates escaped
	// variants). Validation errors contain no configuration or variable values.
	if err = validateEndpoint(ep); err != nil {
		return nil, err
	}
	if len(vars) > maxFields {
		return nil, errors.New("too many variables")
	}
	total := 0
	for k, v := range vars {
		total += len(k) + len(v)
	}
	if total > maxConfigBytes {
		return nil, errors.New("variables exceed size limit")
	}
	redact = redactor(ep, vars)
	req, err := buildRequest(ctx, baseURL, ep, vars)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if ctx.Err() != nil {
		return nil, errors.New("request context canceled before dispatch")
	}
	kind = Unknown
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("upstream request failed")
	}
	defer resp.Body.Close()
	result = &Result{StatusCode: resp.StatusCode}
	defer func() { result.DurationMS = time.Since(start).Milliseconds() }()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	// Do not return truncated bodies: truncation could expose a partial secret.
	if len(raw) > maxBytes {
		return result, errors.New("upstream response exceeds size limit")
	}
	if readErr != nil {
		return result, errors.New("upstream response read failed")
	}
	result.Body = string(raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, errors.New("upstream returned non-success HTTP status")
	}
	result.Mapped, err = MapResponse(ep, raw)
	return result, err
}
func substitute(s string, vars map[string]string, path bool) (string, error) {
	var missing bool
	out := placeholder.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-2]
		v, ok := vars[name]
		if !ok {
			missing = true
			return ""
		}
		if path {
			if v == "." || v == ".." {
				missing = true
				return ""
			}
			return url.PathEscape(v)
		}
		return v
	})
	if missing || strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return "", errors.New("missing variable or invalid template")
	}
	return out, nil
}
func validURL(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == "" && !strings.ContainsAny(u.Host, "\\%")
}
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port))
}
func buildRequest(ctx context.Context, baseURL string, ep Endpoint, vars map[string]string) (*http.Request, error) {
	// Keep this seam safe too: unsupported encodings must never discard a body.
	if err := validateEndpoint(ep); err != nil {
		return nil, err
	}
	base, err := url.Parse(baseURL)
	if err != nil || !validURL(base) || base.RawQuery != "" || base.ForceQuery {
		return nil, errors.New("invalid base URL")
	}
	// Query templates must live in Query, not the URL, to avoid ambiguous escaping.
	if i := strings.IndexByte(ep.Path, '?'); i >= 0 && strings.Contains(ep.Path[i:], "{{") {
		return nil, errors.New("URL query templates must use query fields")
	}
	p, err := substitute(ep.Path, vars, true)
	if err != nil {
		return nil, err
	}
	ref, err := url.Parse(p)
	if err != nil || ref.User != nil || ref.Fragment != "" || strings.HasPrefix(p, "//") {
		return nil, errors.New("invalid endpoint URL")
	}
	target := base.ResolveReference(ref)
	if !validURL(target) || origin(base) != origin(target) {
		return nil, errors.New("cross-origin endpoint is forbidden")
	}
	// Literal targets are rejected even with an injected test transport.
	if ip, err := netip.ParseAddr(target.Hostname()); err == nil && !publicIP(ip) {
		return nil, errors.New("non-public target is forbidden")
	}
	query, err := uniqueValues(target.RawQuery)
	if err != nil {
		return nil, err
	}
	for k, v := range ep.Query {
		if _, exists := query[k]; exists {
			return nil, errors.New("duplicate URL query key")
		}
		s, err := substitute(v, vars, false)
		if err != nil {
			return nil, err
		}
		query[k] = s
	}
	q := url.Values{}
	for k, v := range query {
		q.Set(k, v)
	}
	target.RawQuery = q.Encode()
	body := map[string]string{}
	for k, v := range ep.Body {
		s, err := substitute(v, vars, false)
		if err != nil {
			return nil, err
		}
		body[k] = s
	}
	var encoded string
	switch ep.Encoding {
	case "json":
		var value any = body
		if len(ep.JSONBody) > 0 {
			root, err := decodeJSON(ep.JSONBody)
			if err != nil {
				return nil, err
			}
			value, err = expandJSON(root, vars, &outputBudget{}, 0)
			if err != nil {
				return nil, err
			}
		}
		b, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("invalid JSON body")
		}
		encoded = string(b)
	case "form":
		f := url.Values{}
		for k, v := range body {
			f.Set(k, v)
		}
		encoded = f.Encode()
	}
	if len(encoded) > maxBytes || len(target.String()) > maxConfigBytes {
		return nil, errors.New("request exceeds size limit")
	}
	method := ep.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(encoded))
	if err != nil {
		return nil, errors.New("invalid HTTP request")
	}
	// No GetBody: the transport must never replay a request automatically.
	req.GetBody = nil
	for k, v := range ep.Headers {
		s, err := substitute(v, vars, false)
		if err != nil {
			return nil, err
		}
		if err := validateHeader(k, s); err != nil {
			return nil, err
		}
		req.Header.Set(k, s)
	}
	expected := ""
	switch ep.Encoding {
	case "json":
		expected = "application/json"
	case "form":
		expected = "application/x-www-form-urlencoded"
	}
	if expected != "" {
		if ct := req.Header.Get("Content-Type"); ct != "" {
			media, _, err := mime.ParseMediaType(ct)
			if err != nil || media != expected {
				return nil, errors.New("content type conflicts with encoding")
			}
		} else {
			req.Header.Set("Content-Type", expected)
		}
	}
	return req, nil
}

var deniedPrefixes = func() []netip.Prefix {
	ranges := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "168.63.129.16/32", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}
	out := make([]netip.Prefix, 0, len(ranges))
	for _, s := range ranges {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid dial address")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("upstream DNS resolution failed")
	}
	for _, ip := range addresses {
		if !publicIP(ip) {
			return nil, errors.New("upstream resolves to a forbidden address")
		}
	}
	// Dial a validated IP directly, with no second DNS lookup and no fallback retry.
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	if err != nil {
		return nil, errors.New("upstream connection failed")
	}
	return conn, nil
}
func redactor(ep Endpoint, vars map[string]string) func(string) string {
	secrets := map[string]bool{}
	add := func(v string) {
		if v != "" {
			secrets[v] = true
			secrets[url.QueryEscape(v)] = true
			secrets[url.PathEscape(v)] = true
			b, _ := json.Marshal(v)
			if len(b) >= 2 {
				secrets[string(b[1:len(b)-1])] = true
			}
		}
	}
	for k, v := range vars {
		key := strings.ToLower(k)
		if strings.HasPrefix(key, "secret_") || strings.HasPrefix(key, "credential_") || key == "api_key" || key == "api_secret" || key == "token" || key == "cookie" {
			add(v)
		}
	}
	for k, v := range ep.Headers {
		key := strings.ToLower(k)
		if key != "authorization" && key != "cookie" {
			continue
		}
		expanded, err := substitute(v, vars, false)
		if err != nil {
			expanded = v
		}
		for _, value := range []string{v, expanded} {
			add(value)
			if key == "authorization" {
				if _, token, ok := strings.Cut(value, " "); ok {
					add(token)
				}
			} else {
				for _, part := range strings.Split(value, ";") {
					if _, val, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
						add(val)
					}
				}
			}
		}
	}
	ordered := make([]string, 0, len(secrets))
	for s := range secrets {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	pairs := make([]string, 0, len(ordered)*2)
	for _, s := range ordered {
		pairs = append(pairs, s, "[REDACTED]")
	}
	replacer := strings.NewReplacer(pairs...)
	return replacer.Replace
}
func redactValue(v any, redact func(string) string) any {
	switch x := v.(type) {
	case string:
		return redact(x)
	case json.Number:
		if s := redact(string(x)); s != string(x) {
			return s
		}
		return x
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			m[redact(k)] = redactValue(v, redact)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = redactValue(v, redact)
		}
		return a
	default:
		// Mapped numbers/decimals can also be a credential; preserve type otherwise.
		b, err := json.Marshal(v)
		if err == nil && redact(string(b)) != string(b) {
			return "[REDACTED]"
		}
		return v
	}
}
func redactBody(body string, redact func(string) string) string {
	// Preserve the original body when no redaction is needed. Semantic redaction
	// additionally catches JSON unicode-escaped secrets.
	if v, err := decodeJSON([]byte(body)); err == nil {
		before, beforeErr := json.Marshal(v)
		after, afterErr := json.Marshal(redactValue(v, redact))
		if beforeErr == nil && afterErr == nil && string(before) != string(after) {
			return redact(string(after))
		}
	}
	return redact(body)
}
