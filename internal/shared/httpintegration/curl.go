package httpintegration

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// ParseCurl parses a deliberately restricted literal curl command. It never
// invokes a shell, expands variables, reads files, or resolves network targets.
// Typed/nested JSON is preserved in JSONBody; string-only objects retain Body
// for compatibility with existing import consumers. Duplicate keys are rejected.
func ParseCurl(input string) (RequestSpec, error) {
	s := RequestSpec{Encoding: "none", Headers: map[string]string{}, Query: map[string]string{}, Body: map[string]string{}}
	args, err := curlWords(input)
	if err != nil {
		return s, err
	}
	if len(args) < 2 || args[0] != "curl" {
		return s, errors.New("expected curl command")
	}
	var rawBody string
	hasRaw, hasForm, hasMethod := false, false, false
	setURL := func(v string) error {
		if s.Path != "" {
			return errors.New("multiple URLs are unsupported")
		}
		s.Path = v
		return nil
	}
	setHeader := func(k, v string) error {
		k = http.CanonicalHeaderKey(k)
		if _, ok := s.Headers[k]; ok {
			return errors.New("duplicate header is unsupported")
		}
		if err := validateHeader(k, v); err != nil {
			return err
		}
		s.Headers[k] = v
		return nil
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if err := setURL(a); err != nil {
				return s, err
			}
			continue
		}
		option, value, hasValue := a, "", false
		if strings.HasPrefix(a, "--") {
			if j := strings.IndexByte(a, '='); j >= 0 {
				option, value, hasValue = a[:j], a[j+1:], true
			}
		} else if len(a) > 2 {
			option, value, hasValue = a[:2], a[2:], true
		}
		switch option {
		case "-X", "--request", "-H", "--header", "-d", "--data", "--data-raw", "--data-urlencode", "--url", "-b", "--cookie":
		default:
			return s, errors.New("unsupported curl option")
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return s, errors.New("missing curl option value")
			}
			value = args[i]
		}
		switch option {
		case "-X", "--request":
			if hasMethod {
				return s, errors.New("duplicate method option")
			}
			hasMethod = true
			s.Method = value
		case "--url":
			if err := setURL(value); err != nil {
				return s, err
			}
		case "-H", "--header":
			k, v, ok := strings.Cut(value, ":")
			if !ok || k == "" || strings.HasPrefix(value, "@") {
				return s, errors.New("unsupported header syntax")
			}
			if err := setHeader(k, strings.TrimSpace(v)); err != nil {
				return s, err
			}
		case "-b", "--cookie":
			if !strings.Contains(value, "=") || strings.HasPrefix(value, "@") {
				return s, errors.New("cookie files are unsupported")
			}
			if err := setHeader("Cookie", value); err != nil {
				return s, err
			}
		case "--data-urlencode":
			if hasRaw {
				return s, errors.New("mixed body encodings are unsupported")
			}
			hasForm = true
			k, v, ok := strings.Cut(value, "=")
			if !ok || k == "" || strings.Contains(k, "@") {
				return s, errors.New("only literal name=value data-urlencode is supported")
			}
			if _, ok := s.Body[k]; ok {
				return s, errors.New("duplicate body key")
			}
			s.Body[k] = v
		default:
			if hasRaw || hasForm {
				return s, errors.New("repeated or mixed data options are unsupported")
			}
			if strings.HasPrefix(value, "@") {
				return s, errors.New("file request bodies are unsupported")
			}
			rawBody = value
			hasRaw = true
		}
	}
	if s.Path == "" {
		return s, errors.New("curl URL is required")
	}
	u, err := url.Parse(s.Path)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return s, errors.New("unsupported curl URL")
	}
	if _, err := uniqueValues(u.RawQuery); err != nil {
		return s, err
	}
	if hasRaw || hasForm {
		if s.Method == "" {
			s.Method = http.MethodPost
		}
		media := "application/x-www-form-urlencoded"
		if ct, ok := s.Headers["Content-Type"]; ok {
			var err error
			media, _, err = mime.ParseMediaType(ct)
			if err != nil {
				return s, errors.New("invalid content type")
			}
		}
		switch media {
		case "application/json":
			if hasForm {
				return s, errors.New("data-urlencode cannot encode JSON")
			}
			root, err := decodeJSON([]byte(rawBody))
			if err != nil {
				return s, err
			}
			obj, stringOnly := root.(map[string]any)
			for _, v := range obj {
				if _, ok := v.(string); !ok {
					stringOnly = false
				}
			}
			if stringOnly {
				for k, v := range obj {
					s.Body[k] = v.(string)
				}
			} else {
				s.JSONBody = json.RawMessage(rawBody)
			}
			s.Encoding = "json"
		case "application/x-www-form-urlencoded":
			if hasRaw {
				s.Body, err = uniqueValues(rawBody)
				if err != nil {
					return s, err
				}
			}
			s.Encoding = "form"
		default:
			return s, errors.New("unsupported body content type")
		}
	}
	if s.Method == "" {
		s.Method = http.MethodGet
	}
	ep := Endpoint{Method: s.Method, Path: s.Path, Encoding: s.Encoding, Headers: s.Headers, Query: s.Query, Body: s.Body, JSONBody: s.JSONBody}
	if err := validateEndpoint(ep); err != nil {
		return s, err
	}
	return s, nil
}

// NormalizeCurl converts an imported same-origin URL to a relative endpoint and
// explicit query fields. The target must stay under the configured base path.
// Relative paths honor net/url resolution, including a base without trailing /.
// No DNS lookup or HTTP request is performed.
func NormalizeCurl(baseURL string, spec RequestSpec) (RequestSpec, error) {
	base, err := url.Parse(baseURL)
	if err != nil || !validURL(base) || base.RawQuery != "" || base.ForceQuery {
		return RequestSpec{}, errors.New("invalid base URL")
	}
	ref, err := url.Parse(spec.Path)
	if err != nil || ref.User != nil || ref.Fragment != "" || strings.HasPrefix(spec.Path, "//") {
		return RequestSpec{}, errors.New("invalid imported URL")
	}
	target := base.ResolveReference(ref)
	if !validURL(target) || origin(base) != origin(target) {
		return RequestSpec{}, errors.New("cross-origin imported URL is forbidden")
	}
	prefix := strings.TrimSuffix(base.EscapedPath(), "/")
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if prefix != "" && path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return RequestSpec{}, errors.New("imported URL is outside base path")
	}
	// Escaped traversal can be interpreted by upstream routers after decoding.
	for _, part := range strings.Split(target.Path, "/") {
		if part == "." || part == ".." || strings.Contains(part, "\\") {
			return RequestSpec{}, errors.New("invalid imported path")
		}
	}
	query, err := uniqueValues(target.RawQuery)
	if err != nil {
		return RequestSpec{}, err
	}
	for k, v := range spec.Query {
		if _, exists := query[k]; exists {
			return RequestSpec{}, errors.New("duplicate URL query key")
		}
		query[k] = v
	}
	directory := base.ResolveReference(&url.URL{Path: "./"}).EscapedPath()
	if strings.HasPrefix(path, directory) {
		spec.Path = "./" + strings.TrimPrefix(path, directory)
	} else {
		// Exact base without trailing slash is represented root-relative.
		spec.Path = path
	}
	spec.Query = query
	ep := Endpoint{Method: spec.Method, Path: spec.Path, Encoding: spec.Encoding, Headers: spec.Headers, Query: spec.Query, Body: spec.Body, JSONBody: spec.JSONBody}
	if err := validateEndpoint(ep); err != nil {
		return RequestSpec{}, err
	}
	return spec, nil
}

func uniqueValues(raw string) (map[string]string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("invalid form or query encoding")
	}
	out := map[string]string{}
	for k, v := range values {
		if len(v) != 1 {
			return nil, errors.New("duplicate query or body key")
		}
		out[k] = v[0]
	}
	return out, nil
}
func curlWords(input string) ([]string, error) {
	if len(input) > maxConfigBytes {
		return nil, errors.New("curl input exceeds size limit")
	}
	if strings.ContainsAny(input, "`$\x00\r") {
		return nil, errors.New("shell expansion is unsupported")
	}
	var words []string
	var buf strings.Builder
	var quote byte
	started := false
	flush := func() {
		if started {
			words = append(words, buf.String())
			buf.Reset()
			started = false
		}
	}
	for i := 0; i < len(input); i++ {
		c := input[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		if c == '\\' {
			if i+1 >= len(input) {
				return nil, errors.New("unterminated escape")
			}
			i++
			next := input[i]
			if next == '\n' {
				continue
			}
			if quote == '"' && next != '"' && next != '\\' {
				buf.WriteByte('\\')
			}
			buf.WriteByte(next)
			started = true
			continue
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			started = true
		case ' ', '\t':
			flush()
		case '\n', ';', '|', '&', '<', '>', '(', ')', '#':
			return nil, errors.New("shell operators are unsupported; quote literal URL or data")
		default:
			buf.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	flush()
	if len(words) > maxFields*4 {
		return nil, errors.New("too many curl arguments")
	}
	return words, nil
}
