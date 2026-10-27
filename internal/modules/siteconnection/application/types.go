package application

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/crypto"
	contract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	domain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/httpintegration"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const HTTPSecretMask = "********"
const HTTPMaxBytes = 1 << 20

type HTTPConfigInput struct {
	Config            httpintegration.Config `json:"config"`
	Revision          uint64                 `json:"revision"`
	DeleteCredentials []string               `json:"delete_credentials,omitempty"`
	DeleteEndpoints   []string               `json:"delete_endpoints,omitempty"`
}
type HTTPConfigResult struct {
	Config           httpintegration.Config `json:"config"`
	Configured       bool                   `json:"configured"`
	Revision         uint64                 `json:"revision"`
	Ready            bool                   `json:"ready"`
	ValidationErrors []string               `json:"validation_errors"`
}
type HTTPDebugInput struct {
	Action    string            `json:"action"`
	Endpoint  string            `json:"endpoint"`
	Curl      string            `json:"curl,omitempty"`
	Response  string            `json:"response,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
}
type HTTPDebugResult struct {
	Request *httpintegration.RequestSpec `json:"request,omitempty"`
	Mapped  map[string]any               `json:"mapped,omitempty"`
	Result  *httpintegration.Result      `json:"result,omitempty"`
}

func automatedProtocolAllowed(conn *domain.Connection) error {
	if conn == nil {
		return contract.ErrNotFound
	}
	switch conn.Protocol {
	case constants.ConnectionProtocolDujiaoNext:
		return nil
	case constants.ConnectionProtocolGenericHTTP:
		return contract.ErrHTTPDraftOnly
	default:
		return contract.ErrProtocolUnsupported
	}
}
func (s *Service) httpConnection(id uint) (*domain.Connection, error) {
	conn, err := s.connRepo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, contract.ErrNotFound
	}
	if conn.Protocol != constants.ConnectionProtocolGenericHTTP {
		return nil, contract.ErrProtocolUnsupported
	}
	return conn, nil
}
func (s *Service) loadHTTPConfig(conn *domain.Connection) (httpintegration.Config, error) {
	cfg := httpintegration.Config{Endpoints: map[string]httpintegration.Endpoint{}, Credentials: map[string]string{}}
	if conn.HTTPConfigEncrypted == "" {
		return cfg, nil
	}
	raw, err := crypto.Decrypt(s.encryptKey, conn.HTTPConfigEncrypted)
	if err != nil {
		return cfg, contract.ErrHTTPConfigInvalid
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return cfg, contract.ErrHTTPConfigInvalid
	}
	if cfg.Endpoints == nil {
		cfg.Endpoints = map[string]httpintegration.Endpoint{}
	}
	if cfg.Credentials == nil {
		cfg.Credentials = map[string]string{}
	}
	return cfg, nil
}
func (s *Service) GetHTTPConfig(id uint) (*HTTPConfigResult, error) {
	conn, err := s.httpConnection(id)
	if err != nil {
		return nil, err
	}
	cfg, err := s.loadHTTPConfig(conn)
	if err != nil {
		return nil, err
	}
	if err = normalizeHTTPConfig(&cfg); err != nil {
		return nil, err
	}
	return s.httpConfigResult(conn, cfg), nil
}
func (s *Service) httpConfigResult(conn *domain.Connection, cfg httpintegration.Config) *HTTPConfigResult {
	result := &HTTPConfigResult{Config: cfg, Configured: conn.HTTPConfigEncrypted != "", Revision: conn.HTTPConfigRevision, ValidationErrors: []string{}}
	if s.httpValidator == nil || s.httpFactory == nil {
		result.ValidationErrors = append(result.ValidationErrors, "httpIntegration.runtimeUnavailable")
	} else if err := s.httpValidator(cfg); err != nil {
		if cfg.Version != 1 {
			result.ValidationErrors = append(result.ValidationErrors, "httpIntegration.versionRequired")
		}
		for _, requirement := range []struct{ endpoint, key string }{{"products", "missingProducts"}, {"product", "missingProduct"}, {"create_order", "missingCreateOrder"}} {
			if _, ok := cfg.Endpoints[requirement.endpoint]; !ok {
				result.ValidationErrors = append(result.ValidationErrors, "httpIntegration."+requirement.key)
			}
		}
		_, query := cfg.Endpoints["get_order"]
		create := cfg.Endpoints["create_order"]
		_, fulfillment := create.Fields["fulfillment"]
		_, payload := create.Fields["payload"]
		_, cards := create.Fields["cards"]
		if !query && !fulfillment && !payload && !cards {
			result.ValidationErrors = append(result.ValidationErrors, "httpIntegration.missingDelivery")
		}
		for _, name := range []string{"products", "product", "create_order", "get_order", "lookup_order"} {
			ep, exists := cfg.Endpoints[name]
			if !exists {
				continue
			}
			keys := []string{"order_id", "status", "amount", "currency"}
			if name == "products" || name == "product" {
				keys = []string{"id", "title", "currency", "is_active"}
				_, collection := ep.Collections["skus"]
				_, rawSKUs := ep.Fields["skus"]
				if !collection && !rawSKUs {
					keys = append(keys, "stock_quantity")
					_, amount := ep.Fields["price_amount"]
					_, price := ep.Fields["price"]
					if !amount && !price {
						keys = append(keys, "price_amount")
					}
				}
			}
			for _, key := range keys {
				if _, exists := ep.Fields[key]; !exists {
					result.ValidationErrors = append(result.ValidationErrors, "httpIntegration.missingField|"+name+"|"+key)
				}
			}
		}
		if len(result.ValidationErrors) == 0 {
			result.ValidationErrors = append(result.ValidationErrors, "httpIntegration.incompleteConfiguration")
		}
	} else {
		result.Ready = true
	}
	for k := range result.Config.Credentials {
		result.Config.Credentials[k] = HTTPSecretMask
	}
	return result
}
func (s *Service) PutHTTPConfig(id uint, input HTTPConfigInput) (*HTTPConfigResult, error) {
	conn, err := s.httpConnection(id)
	if err != nil {
		return nil, err
	}
	if input.Revision != conn.HTTPConfigRevision {
		return nil, contract.ErrHTTPConfigConflict
	}
	old, err := s.loadHTTPConfig(conn)
	if err != nil {
		return nil, err
	}
	// Clone caller maps before normalization; masked or omitted credentials retain old values.
	raw, err := json.Marshal(input.Config)
	if err != nil || len(raw) > HTTPMaxBytes {
		return nil, contract.ErrHTTPConfigInvalid
	}
	var cfg httpintegration.Config
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, contract.ErrHTTPConfigInvalid
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Version != 1 {
		return nil, contract.ErrHTTPConfigInvalid
	}
	if cfg.Endpoints == nil {
		cfg.Endpoints = map[string]httpintegration.Endpoint{}
	}
	if cfg.Credentials == nil {
		cfg.Credentials = map[string]string{}
	}
	for k, v := range cfg.Credentials {
		if v == HTTPSecretMask {
			previous, ok := old.Credentials[k]
			if !ok {
				return nil, contract.ErrHTTPConfigInvalid
			}
			cfg.Credentials[k] = previous
		}
	}
	for k, v := range old.Credentials {
		if _, ok := cfg.Credentials[k]; !ok {
			cfg.Credentials[k] = v
		}
	}
	for _, name := range input.DeleteCredentials {
		if !credentialName.MatchString(name) {
			return nil, contract.ErrHTTPConfigInvalid
		}
		delete(cfg.Credentials, name)
	}
	for _, name := range input.DeleteEndpoints {
		delete(cfg.Endpoints, name)
	}
	if err = normalizeHTTPConfig(&cfg); err != nil {
		return nil, err
	}
	for _, ep := range cfg.Endpoints {
		rawEndpoint, marshalErr := json.Marshal(ep)
		if marshalErr != nil {
			return nil, contract.ErrHTTPConfigInvalid
		}
		for _, ref := range templateReference.FindAllString(string(rawEndpoint), -1) {
			name := strings.TrimSuffix(strings.TrimPrefix(ref, "{{"), "}}")
			if strings.HasPrefix(name, "credential_") {
				if _, exists := cfg.Credentials[strings.TrimPrefix(name, "credential_")]; !exists {
					return nil, contract.ErrHTTPConfigInvalid
				}
			}
		}
	}
	if len(cfg.Endpoints) > 0 && cfg.Validate() != nil {
		return nil, contract.ErrHTTPConfigInvalid
	}
	raw, err = json.Marshal(cfg)
	if err != nil || len(raw) > HTTPMaxBytes {
		return nil, contract.ErrHTTPConfigInvalid
	}
	conn.HTTPConfigEncrypted, err = crypto.Encrypt(s.encryptKey, string(raw))
	if err != nil {
		return nil, err
	}
	updated, err := s.connRepo.UpdateHTTPConfig(id, input.Revision, conn.HTTPConfigEncrypted)
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, contract.ErrHTTPConfigConflict
	}
	conn.HTTPConfigRevision++
	return s.httpConfigResult(conn, cfg), nil
}

var credentialName = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
var templateReference = regexp.MustCompile(`\{\{[^{}]+\}\}`)

func sensitiveName(key string) bool {
	k := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, part := range []string{"authorization", "cookie", "apikey", "apisecret", "token", "password", "secret", "credential"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}
func replaceOutsideReferences(value string, replace func(string) string) string {
	matches := templateReference.FindAllStringIndex(value, -1)
	var b strings.Builder
	start := 0
	for _, m := range matches {
		b.WriteString(replace(value[start:m[0]]))
		b.WriteString(value[m[0]:m[1]])
		start = m[1]
	}
	b.WriteString(replace(value[start:]))
	return b.String()
}
func normalizeHTTPConfig(cfg *httpintegration.Config) error {
	if cfg.Credentials == nil {
		cfg.Credentials = map[string]string{}
	}
	for k, v := range cfg.Credentials {
		if !credentialName.MatchString(k) || v == HTTPSecretMask {
			return contract.ErrHTTPConfigInvalid
		}
	}
	// Extract inline credentials, including URL query parameters, before persistence.
	for name, ep := range cfg.Endpoints {
		if ep.Query == nil {
			ep.Query = map[string]string{}
		}
		if i := strings.IndexByte(ep.Path, '?'); i >= 0 {
			q, err := url.ParseQuery(ep.Path[i+1:])
			if err != nil {
				return contract.ErrHTTPConfigInvalid
			}
			for k, v := range q {
				if len(v) != 1 {
					return contract.ErrHTTPConfigInvalid
				}
				if _, ok := ep.Query[k]; ok {
					return contract.ErrHTTPConfigInvalid
				}
				ep.Query[k] = v[0]
			}
			ep.Path = ep.Path[:i]
		}
		if u, err := url.Parse(ep.Path); err == nil && u.User != nil {
			return contract.ErrHTTPConfigInvalid
		}
		for _, m := range []map[string]string{ep.Headers, ep.Query, ep.Body} {
			for k, v := range m {
				if v == HTTPSecretMask {
					return contract.ErrHTTPConfigInvalid
				}
				if !sensitiveName(k) || v == "" {
					continue
				}
				if templateReference.MatchString(v) {
					literal := strings.TrimSpace(templateReference.ReplaceAllString(v, ""))
					if literal != "" && literal != "Bearer" && literal != "Basic" {
						return contract.ErrHTTPConfigInvalid
					}
					continue
				}
				key := ""
				for existing, secret := range cfg.Credentials {
					if secret == v {
						key = existing
						break
					}
				}
				if key == "" {
					for n := 1; ; n++ {
						candidate := fmt.Sprintf("inline_%d", n)
						if _, ok := cfg.Credentials[candidate]; !ok {
							key = candidate
							break
						}
					}
					cfg.Credentials[key] = v
				}
				m[k] = "{{credential_" + key + "}}"
			}
		}
		if len(ep.JSONBody) > 0 {
			var value any
			decoder := json.NewDecoder(strings.NewReader(string(ep.JSONBody)))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return contract.ErrHTTPConfigInvalid
			}
			var protect func(any, int) error
			protect = func(node any, depth int) error {
				if depth > 32 {
					return contract.ErrHTTPConfigInvalid
				}
				switch item := node.(type) {
				case map[string]any:
					for key, value := range item {
						if sensitiveName(key) && value != nil {
							text, ok := value.(string)
							if !ok {
								return contract.ErrHTTPConfigInvalid
							}
							if text == HTTPSecretMask {
								return contract.ErrHTTPConfigInvalid
							}
							if text == "" {
								continue
							}
							if templateReference.MatchString(text) {
								literal := strings.TrimSpace(templateReference.ReplaceAllString(text, ""))
								if literal != "" && literal != "Bearer" && literal != "Basic" {
									return contract.ErrHTTPConfigInvalid
								}
								continue
							}
							name := ""
							for existing, secret := range cfg.Credentials {
								if secret == text {
									name = existing
									break
								}
							}
							if name == "" {
								for n := 1; ; n++ {
									candidate := fmt.Sprintf("inline_%d", n)
									if _, exists := cfg.Credentials[candidate]; !exists {
										name = candidate
										break
									}
								}
								cfg.Credentials[name] = text
							}
							item[key] = "{{credential_" + name + "}}"
						} else if err := protect(value, depth+1); err != nil {
							return err
						}
					}
				case []any:
					for _, value := range item {
						if err := protect(value, depth+1); err != nil {
							return err
						}
					}
				}
				return nil
			}
			if err := protect(value, 0); err != nil {
				return err
			}
			ep.JSONBody, _ = json.Marshal(value)
		}
		cfg.Endpoints[name] = ep
	}
	// Replace credential values in arbitrary request fields, preserving references.
	keys := make([]string, 0, len(cfg.Credentials))
	for k, v := range cfg.Credentials {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(cfg.Credentials[keys[i]]) > len(cfg.Credentials[keys[j]]) })
	pairs := []string{}
	for _, k := range keys {
		v := cfg.Credentials[k]
		pairs = append(pairs, v, "{{credential_"+k+"}}")
		if escaped := url.QueryEscape(v); escaped != v {
			pairs = append(pairs, escaped, "{{credential_"+k+"}}")
		}
	}
	replacer := strings.NewReplacer(pairs...)
	apply := func(v string) string { return replaceOutsideReferences(v, replacer.Replace) }
	for name, ep := range cfg.Endpoints {
		ep.Path = apply(ep.Path)
		for _, m := range []map[string]string{ep.Headers, ep.Query, ep.Body} {
			for k, v := range m {
				m[k] = apply(v)
			}
		}
		ep.Success.Equals = apply(ep.Success.Equals)
		for k, f := range ep.Fields {
			f.Value = apply(f.Value)
			f.Default = apply(f.Default)
			for ek, ev := range f.Enum {
				f.Enum[ek] = apply(ev)
			}
			ep.Fields[k] = f
		}
		// Process the encoded endpoint structurally so new collection/meta/JSON fields
		// cannot expose a known literal secret, while preserving JSON number types.
		rawEndpoint, err := json.Marshal(ep)
		if err != nil {
			return contract.ErrHTTPConfigInvalid
		}
		var tree any
		decoder := json.NewDecoder(strings.NewReader(string(rawEndpoint)))
		decoder.UseNumber()
		if decoder.Decode(&tree) != nil {
			return contract.ErrHTTPConfigInvalid
		}
		var replace func(any) any
		replace = func(v any) any {
			switch node := v.(type) {
			case string:
				return apply(node)
			case map[string]any:
				for k, child := range node {
					node[k] = replace(child)
				}
			case []any:
				for i, child := range node {
					node[i] = replace(child)
				}
			}
			return v
		}
		rawEndpoint, err = json.Marshal(replace(tree))
		if err != nil || json.Unmarshal(rawEndpoint, &ep) != nil {
			return contract.ErrHTTPConfigInvalid
		}
		cfg.Endpoints[name] = ep
	}
	return nil
}
func (s *Service) HTTPDebug(ctx context.Context, id uint, input HTTPDebugInput) (*HTTPDebugResult, error) {
	conn, err := s.httpConnection(id)
	if err != nil {
		return nil, err
	}
	if len(input.Curl) > 32<<10 || len(input.Response) > HTTPMaxBytes || input.Endpoint == "" {
		return nil, contract.ErrHTTPConfigInvalid
	}
	if input.Action == "parse_curl" {
		spec, err := httpintegration.ParseCurl(input.Curl)
		if err == nil {
			spec, err = httpintegration.NormalizeCurl(conn.BaseURL, spec)
		}
		if err != nil {
			return nil, contract.ErrHTTPConfigInvalid
		}
		// The editor uses root-relative paths; ResolveReference retains the exact
		// imported path without joining a configured base prefix twice.
		base, baseErr := url.Parse(conn.BaseURL)
		ref, pathErr := url.Parse(spec.Path)
		if baseErr != nil || pathErr != nil {
			return nil, contract.ErrHTTPConfigInvalid
		}
		spec.Path = base.ResolveReference(ref).EscapedPath()
		if spec.Path == "" {
			spec.Path = "/"
		}
		return &HTTPDebugResult{Request: &spec}, nil
	}
	cfg, err := s.loadHTTPConfig(conn)
	if err != nil {
		return nil, err
	}
	ep, ok := cfg.Endpoints[input.Endpoint]
	if !ok {
		return nil, contract.ErrHTTPConfigInvalid
	}
	switch input.Action {
	case "map_response":
		mapped, err := httpintegration.MapResponse(ep, []byte(input.Response))
		if err != nil {
			return nil, contract.ErrHTTPDebugFailed
		}
		return redactDebugResult(&HTTPDebugResult{Mapped: mapped}, cfg, input.Variables)
	case "test":
		switch input.Endpoint {
		case "ping", "balance", "products", "product":
		default:
			return nil, contract.ErrHTTPConfigInvalid
		}
		variables := map[string]string{}
		for k, v := range input.Variables {
			if strings.HasPrefix(k, "credential_") {
				return nil, contract.ErrHTTPConfigInvalid
			}
			variables[k] = v
		}
		for k, v := range cfg.Credentials {
			variables["credential_"+k] = v
		}
		result, err := httpintegration.Execute(ctx, conn.BaseURL, ep, variables)
		if err != nil {
			if result == nil {
				return nil, contract.ErrHTTPDebugFailed
			}
			result.Error = "upstream_response_invalid"
		}
		return redactDebugResult(&HTTPDebugResult{Result: result}, cfg, input.Variables)
	default:
		return nil, contract.ErrHTTPConfigInvalid
	}
}
func redactDebugResult(result *HTTPDebugResult, cfg httpintegration.Config, variables map[string]string) (*HTTPDebugResult, error) {
	secrets := []string{}
	add := func(v string) {
		if v != "" {
			secrets = append(secrets, v, url.QueryEscape(v), url.PathEscape(v))
			if _, token, ok := strings.Cut(v, " "); ok {
				secrets = append(secrets, token)
			}
			for _, p := range strings.Split(v, ";") {
				if _, token, ok := strings.Cut(p, "="); ok {
					secrets = append(secrets, token)
				}
			}
		}
	}
	for _, v := range cfg.Credentials {
		add(v)
	}
	for k, v := range variables {
		if sensitiveName(k) {
			add(v)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := []string{}
	for _, v := range secrets {
		if v != "" {
			pairs = append(pairs, v, "[REDACTED]")
		}
	}
	replace := strings.NewReplacer(pairs...).Replace
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				switch nested.(type) {
				case map[string]any, []any:
					b, _ := json.Marshal(walk(nested))
					return replace(string(b))
				}
			}
			return replace(x)
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				out[replace(k)] = walk(v)
			}
			return out
		case []any:
			for i, v := range x {
				x[i] = walk(v)
			}
			return x
		case json.Number:
			if replace(string(x)) != string(x) {
				return "[REDACTED]"
			}
			return x
		default:
			return v
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, contract.ErrHTTPDebugFailed
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, contract.ErrHTTPDebugFailed
	}
	// Metadata stays numeric; only payloads require recursive redaction.
	object := value.(map[string]any)
	if mapped, ok := object["mapped"]; ok {
		object["mapped"] = walk(mapped)
	}
	if r, ok := object["result"].(map[string]any); ok {
		r["body"] = walk(r["body"])
		if m, ok := r["mapped"]; ok {
			r["mapped"] = walk(m)
		}
	}
	raw, err = json.Marshal(object)
	if err != nil {
		return nil, contract.ErrHTTPDebugFailed
	}
	var out HTTPDebugResult
	if json.Unmarshal(raw, &out) != nil {
		return nil, contract.ErrHTTPDebugFailed
	}
	return &out, nil
}

// CreateInput 创建对接连接输入。
type CreateInput struct {
	Name               string  `json:"name"`
	BaseURL            string  `json:"base_url"`
	ApiKey             string  `json:"api_key"`
	ApiSecret          string  `json:"api_secret"`
	Protocol           string  `json:"protocol"`
	CallbackURL        string  `json:"callback_url"`
	RetryMax           int     `json:"retry_max"`
	RetryIntervals     string  `json:"retry_intervals"`
	ExchangeRate       float64 `json:"exchange_rate"`
	PriceMarkupPercent float64 `json:"price_markup_percent"`
	PriceRoundingMode  string  `json:"price_rounding_mode"`
	AutoSyncPrice      bool    `json:"auto_sync_price"`
}

// UpdateInput 更新对接连接输入。
type UpdateInput struct {
	Name               string   `json:"name"`
	BaseURL            string   `json:"base_url"`
	ApiKey             string   `json:"api_key"`
	ApiSecret          string   `json:"api_secret"` // 为空则不更新
	Protocol           string   `json:"protocol"`
	CallbackURL        string   `json:"callback_url"`
	RetryMax           int      `json:"retry_max"`
	RetryIntervals     string   `json:"retry_intervals"`
	ExchangeRate       *float64 `json:"exchange_rate"`
	PriceMarkupPercent *float64 `json:"price_markup_percent"` // 指针类型，区分 0 和未传
	PriceRoundingMode  *string  `json:"price_rounding_mode"`
	AutoSyncPrice      *bool    `json:"auto_sync_price"`
}

// PingResult 连接测试结果。
type PingResult struct {
	SiteName        string                 `json:"site_name"`
	ProtocolVersion string                 `json:"protocol_version"`
	UserID          uint                   `json:"user_id"`
	Balance         string                 `json:"balance"`
	Currency        string                 `json:"currency"`
	MemberLevel     map[string]interface{} `json:"member_level,omitempty"`
}
