// Package httpintegration provides bounded, declarative HTTP integration primitives.
// It has no business, persistence, or transport-framework dependencies.
package httpintegration

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxBytes       = 1 << 20
	maxConfigBytes = 256 << 10
	maxFields      = 128
	maxDepth       = 32
	maxNodes       = 10000
)

type Config struct {
	Version     int                 `json:"version,omitempty"`
	Endpoints   map[string]Endpoint `json:"endpoints"`
	Credentials map[string]string   `json:"credentials,omitempty"`
}
type Endpoint struct {
	Method      string                `json:"method"`
	Path        string                `json:"path"`
	Encoding    string                `json:"encoding"`
	Headers     map[string]string     `json:"headers"`
	Query       map[string]string     `json:"query"`
	JSONBody    json.RawMessage       `json:"json_body,omitempty"`
	Body        map[string]string     `json:"body"`
	Success     Condition             `json:"success"`
	Fields      map[string]Field      `json:"fields"`
	ListPath    string                `json:"list_path"`
	List        bool                  `json:"list,omitempty"`
	MetaFields  map[string]Field      `json:"meta_fields,omitempty"`
	Collections map[string]Collection `json:"collections,omitempty"`
}

// Collection maps an array relative to each item (or the root for non-list endpoints).
// Missing or non-array collections fail rather than silently dropping SKU data.
type Collection struct {
	Path   string           `json:"path"`
	Fields map[string]Field `json:"fields"`
}
type Condition struct {
	Path string `json:"path"`
	// Equals retains the Go string API for existing configuration normalizers.
	// JSON decoding also accepts typed scalars; see scalar_config.go.
	Equals string `json:"-"`
	equals optionalScalar
}
type Field struct {
	Path      string            `json:"path"`
	Value     string            `json:"-"`
	Default   string            `json:"-"`
	Required  bool              `json:"required"`
	Transform string            `json:"transform"`
	Enum      map[string]string `json:"enum"`
	value     optionalScalar
	fallback  optionalScalar
}
type RequestSpec struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Encoding string            `json:"encoding"`
	Headers  map[string]string `json:"headers"`
	Query    map[string]string `json:"query"`
	JSONBody json.RawMessage   `json:"json_body,omitempty"`
	Body     map[string]string `json:"body"`
}
type Result struct {
	Error      string         `json:"error,omitempty"`
	StatusCode int            `json:"status_code"`
	DurationMS int64          `json:"duration_ms"`
	Body       string         `json:"body"`
	Mapped     map[string]any `json:"mapped,omitempty"`
}

var pathPart = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (c Config) Validate() error {
	if len(c.Endpoints) == 0 || len(c.Endpoints) > 32 || len(c.Credentials) > maxFields {
		return errors.New("invalid configuration size")
	}
	b, err := json.Marshal(c)
	if err != nil || len(b) > maxConfigBytes {
		return errors.New("configuration exceeds size limit")
	}
	for name, ep := range c.Endpoints {
		if !pathPart.MatchString(name) {
			return errors.New("invalid endpoint name")
		}
		if err := validateEndpoint(ep); err != nil {
			return err
		}
	}
	return nil
}

func validatePath(p string) error {
	if p == "" {
		return nil
	}
	parts := strings.Split(p, ".")
	if len(parts) > maxDepth {
		return errors.New("mapping path too deep")
	}
	for _, s := range parts {
		if !pathPart.MatchString(s) {
			return errors.New("unsupported mapping path")
		}
	}
	return nil
}
func validateMapping(ep Endpoint) error {
	b, err := json.Marshal(ep)
	if err != nil || len(b) > maxConfigBytes {
		return errors.New("endpoint exceeds size limit")
	}
	if len(ep.Fields)+len(ep.MetaFields)+len(ep.Collections) > maxFields {
		return errors.New("too many mapped fields")
	}
	if len(ep.MetaFields) > 0 && !ep.List && ep.ListPath == "" {
		return errors.New("metadata requires a list endpoint")
	}
	if _, exists := ep.MetaFields["items"]; exists {
		return errors.New("metadata cannot overwrite items")
	}
	groups := []map[string]Field{ep.Fields, ep.MetaFields}
	totalFields := len(ep.Fields) + len(ep.MetaFields) + len(ep.Collections)
	for name, collection := range ep.Collections {
		if name == "" || len(name) > 256 {
			return errors.New("invalid collection name")
		}
		if _, exists := ep.Fields[name]; exists {
			return errors.New("collection conflicts with mapped field")
		}
		if err := validatePath(collection.Path); err != nil {
			return err
		}
		totalFields += len(collection.Fields)
		groups = append(groups, collection.Fields)
	}
	if totalFields > maxFields {
		return errors.New("too many mapped fields")
	}
	_, hasEquals := ep.Success.equals.get(ep.Success.Equals)
	if (ep.Success.Path != "") != hasEquals {
		return errors.New("success condition requires path and equals")
	}
	if err := validatePath(ep.Success.Path); err != nil {
		return err
	}
	if err := validatePath(ep.ListPath); err != nil {
		return err
	}
	for _, fields := range groups {
		if err := validateFields(fields); err != nil {
			return err
		}
	}
	return nil
}
func validateFields(fields map[string]Field) error {
	for k, f := range fields {
		if k == "" || len(k) > 256 || len(f.Enum) > maxFields {
			return errors.New("invalid field configuration")
		}
		if err := validatePath(f.Path); err != nil {
			return err
		}
		value, hasValue := f.value.get(f.Value)
		// Old configurations serialized an empty value alongside every path.
		if f.Path != "" && hasValue && value != "" {
			return errors.New("field cannot have both path and value")
		}
		switch f.Transform {
		case "", "string", "int", "bool", "decimal", "cents_to_units":
		default:
			return errors.New("unsupported field transform")
		}
	}
	return nil
}
func validateEndpoint(ep Endpoint) error {
	b, err := json.Marshal(ep)
	if err != nil || len(b) > maxConfigBytes {
		return errors.New("endpoint exceeds size limit")
	}
	if ep.Path == "" {
		return errors.New("endpoint path is required")
	}
	switch ep.Method {
	case "", http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
	default:
		return errors.New("unsupported HTTP method")
	}
	switch ep.Encoding {
	case "", "none", "json", "form":
	default:
		return errors.New("unsupported request encoding")
	}
	if len(ep.JSONBody) > 0 {
		if ep.Encoding != "json" || len(ep.Body) > 0 {
			return errors.New("json_body requires JSON encoding and no legacy body")
		}
		if _, err := decodeJSON(ep.JSONBody); err != nil {
			return err
		}
	}
	if len(ep.Body) > 0 && (ep.Encoding == "" || ep.Encoding == "none") {
		return errors.New("body requires explicit encoding")
	}
	for _, m := range []map[string]string{ep.Headers, ep.Query, ep.Body} {
		if len(m) > maxFields {
			return errors.New("too many request fields")
		}
	}
	seen := map[string]bool{}
	for k, v := range ep.Headers {
		key := strings.ToLower(k)
		if seen[key] {
			return errors.New("duplicate request header")
		}
		seen[key] = true
		if err := validateHeader(k, v); err != nil {
			return err
		}
	}
	return validateMapping(ep)
}
func validateHeader(k, v string) error {
	if !headerName.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") {
		return errors.New("invalid HTTP header")
	}
	switch strings.ToLower(k) {
	case "host", "content-length", "transfer-encoding", "connection", "proxy-connection", "proxy-authorization", "proxy-authenticate", "trailer", "te", "upgrade", "expect", "accept-encoding":
		return errors.New("unsafe HTTP header")
	}
	return nil
}
