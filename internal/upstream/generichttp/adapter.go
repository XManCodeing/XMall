// Package generichttp adapts declarative HTTP integrations to the upstream contract.
package generichttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/shared/httpintegration"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/upstream"
)

var ErrUnsupported = errors.New("generic HTTP operation is not supported")

// CapabilityReport describes configured operations, not upstream guarantees.
type CapabilityReport struct {
	Ping                   bool `json:"ping"`
	Balance                bool `json:"balance"`
	Products               bool `json:"products"`
	Product                bool `json:"product"`
	CreateOrder            bool `json:"create_order"`
	GetOrder               bool `json:"get_order"`
	LookupOrder            bool `json:"lookup_order"`
	CancelOrder            bool `json:"cancel_order"`
	SynchronousFulfillment bool `json:"synchronous_fulfillment"`
}

func Capabilities(cfg httpintegration.Config) CapabilityReport {
	has := func(name string) bool { _, ok := cfg.Endpoints[name]; return ok }
	return CapabilityReport{has("ping"), has("balance"), has("products"), has("product"), has("create_order"), has("get_order"), has("lookup_order"), has("cancel_order"), hasDelivery(cfg.Endpoints["create_order"])}
}
func hasDelivery(ep httpintegration.Endpoint) bool {
	for _, key := range []string{"fulfillment", "payload", "cards"} {
		if _, ok := ep.Fields[key]; ok {
			return true
		}
	}
	return false
}

var variableName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Validate checks the business contract in addition to the shared engine limits.
func Validate(cfg httpintegration.Config) error {
	if cfg.Version != 1 {
		return errors.New("generic HTTP configuration version must be 1")
	}
	if err := cfg.Validate(); err != nil {
		return errors.New("invalid generic HTTP configuration")
	}
	for name := range cfg.Credentials {
		if !variableName.MatchString(name) {
			return errors.New("invalid credential name")
		}
	}
	allowed := map[string]bool{"ping": true, "balance": true, "products": true, "product": true, "create_order": true, "get_order": true, "lookup_order": true, "cancel_order": true}
	for name, ep := range cfg.Endpoints {
		if !allowed[name] {
			return errors.New("unsupported generic HTTP endpoint")
		}
		if c, ok := ep.Collections["skus"]; ok {
			for _, key := range []string{"id", "stock_quantity"} {
				if _, ok := c.Fields[key]; !ok {
					return errors.New("SKU collection requires id and stock_quantity mappings")
				}
			}
			_, amount := c.Fields["price_amount"]
			_, price := c.Fields["price"]
			if !amount && !price {
				return errors.New("SKU collection requires price mapping")
			}
		}
		groups := []map[string]httpintegration.Field{ep.Fields}
		for _, collection := range ep.Collections {
			groups = append(groups, collection.Fields)
		}
		for _, fields := range groups {
			for _, key := range []string{"id", "order_id", "order_no"} {
				if f, ok := fields[key]; ok && ((f.Transform != "" && f.Transform != "string") || len(f.Enum) > 0) {
					return errors.New("identifier mappings must preserve exact source values")
				}
			}
		}
	}
	for _, name := range []string{"products", "product", "create_order"} {
		if _, ok := cfg.Endpoints[name]; !ok {
			return fmt.Errorf("%s endpoint is required", name)
		}
	}
	ep := cfg.Endpoints["products"]
	if ep.ListPath == "" && !ep.List {
		return errors.New("products must be a list endpoint")
	}
	for _, name := range []string{"product", "create_order", "get_order", "lookup_order", "cancel_order", "ping", "balance"} {
		if ep, ok := cfg.Endpoints[name]; ok && (ep.List || ep.ListPath != "") {
			return errors.New("only products can be a list endpoint")
		}
	}
	for _, name := range []string{"products", "product"} {
		ep := cfg.Endpoints[name]
		fields := ep.Fields
		for _, key := range []string{"id", "title", "currency", "is_active"} {
			if _, ok := fields[key]; !ok {
				return fmt.Errorf("%s requires %s mapping", name, key)
			}
		}
		_, rawSKUs := fields["skus"]
		_, mappedSKUs := ep.Collections["skus"]
		if !rawSKUs && !mappedSKUs {
			if _, ok := fields["stock_quantity"]; !ok {
				return errors.New("single product requires stock_quantity mapping")
			}
			_, amount := fields["price_amount"]
			_, price := fields["price"]
			if !amount && !price {
				return errors.New("single product requires price mapping")
			}
		}
	}
	for _, name := range []string{"create_order", "get_order", "lookup_order"} {
		if ep, ok := cfg.Endpoints[name]; ok {
			for _, key := range []string{"order_id", "status", "amount", "currency"} {
				if _, ok := ep.Fields[key]; !ok {
					return fmt.Errorf("%s requires %s mapping", name, key)
				}
			}
		}
	}
	if ep, ok := cfg.Endpoints["balance"]; ok {
		for _, key := range []string{"balance", "currency"} {
			if _, ok := ep.Fields[key]; !ok {
				return errors.New("balance requires balance and currency mappings")
			}
		}
	}
	if ep, ok := cfg.Endpoints["cancel_order"]; ok {
		if _, ok := ep.Fields["status"]; !ok {
			return errors.New("cancel_order requires status mapping")
		}
	}
	if _, ok := cfg.Endpoints["get_order"]; !ok && !hasDelivery(cfg.Endpoints["create_order"]) {
		return errors.New("get_order or synchronous fulfillment mapping is required")
	}
	return nil
}

type executor func(context.Context, string, httpintegration.Endpoint, map[string]string) (*httpintegration.Result, error)
type adapter struct {
	baseURL string
	cfg     httpintegration.Config
	execute executor
}

var _ upstream.Adapter = (*adapter)(nil)
var _ upstream.ExternalProductAdapter = (*adapter)(nil)
var _ upstream.ExternalOrderAdapter = (*adapter)(nil)
var _ upstream.OrderLookupAdapter = (*adapter)(nil)

// New always uses the SSRF-protected business executor. The executor seam is private.
func New(baseURL string, cfg httpintegration.Config) (upstream.Adapter, error) {
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" {
		return nil, errors.New("invalid generic HTTP base URL")
	}
	// Own the maps, so subsequent caller mutation cannot change credentials or mappings.
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, errors.New("invalid generic HTTP configuration")
	}
	var owned httpintegration.Config
	if json.Unmarshal(raw, &owned) != nil {
		return nil, errors.New("invalid generic HTTP configuration")
	}
	return &adapter{baseURL: baseURL, cfg: owned, execute: httpintegration.ExecuteBusiness}, nil
}
func (a *adapter) call(ctx context.Context, name string, vars map[string]string) (map[string]any, error) {
	ep, ok := a.cfg.Endpoints[name]
	if !ok {
		return nil, ErrUnsupported
	}
	merged := make(map[string]string, len(vars)+len(a.cfg.Credentials))
	for k, v := range vars {
		merged[k] = v
	}
	for k, v := range a.cfg.Credentials {
		key := "credential_" + k
		if _, exists := merged[key]; exists {
			return nil, errors.New("credential variable collision")
		}
		merged[key] = v
	}
	result, err := a.execute(ctx, a.baseURL, ep, merged)
	// Never expose response bodies, configured secrets, or executor errors to callers.
	if err != nil || result == nil || result.StatusCode < 200 || result.StatusCode >= 300 || result.Error != "" || result.Mapped == nil {
		return nil, fmt.Errorf("generic HTTP %s request failed", name)
	}
	return result.Mapped, nil
}
func (a *adapter) Ping(ctx context.Context) (*upstream.PingResult, error) {
	name := "ping"
	if _, ok := a.cfg.Endpoints[name]; !ok {
		name = "balance"
	}
	m, err := a.call(ctx, name, nil)
	if err != nil {
		return nil, err
	}
	result := &upstream.PingResult{ProtocolVersion: "generic-http/1"}
	if v, ok := m["site_name"]; ok {
		result.SiteName, err = text(v)
		if err != nil {
			return nil, err
		}
	}
	if name == "ping" {
		if _, configured := a.cfg.Endpoints["balance"]; configured {
			balance, e := a.call(ctx, "balance", nil)
			if e != nil {
				return nil, e
			}
			if _, exists := balance["balance"]; !exists {
				return nil, errors.New("balance response is missing amount")
			}
			m = balance
		}
	}
	if name == "balance" {
		if _, exists := m["balance"]; !exists {
			return nil, errors.New("balance response is missing amount")
		}
	}
	if _, ok := m["balance"]; ok {
		result.Balance, err = money(m["balance"])
		if err != nil {
			return nil, err
		}
		result.Currency, err = currency(m["currency"])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (*adapter) ListCategories(context.Context) (*upstream.CategoryListResult, error) {
	return &upstream.CategoryListResult{Supported: false, Categories: []upstream.UpstreamCategory{}}, nil
}
func (*adapter) DownloadImage(context.Context, string) (string, error) { return "", ErrUnsupported }
func (a *adapter) ListProducts(ctx context.Context, opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
	if opts.Page < 0 || opts.PageSize < 0 || opts.Offset < 0 {
		return nil, errors.New("invalid pagination")
	}
	vars := map[string]string{"page": strconv.Itoa(opts.Page), "page_size": strconv.Itoa(opts.PageSize), "offset": strconv.Itoa(opts.Offset), "cursor": opts.Cursor, "include_inactive": strconv.FormatBool(opts.IncludeInactive)}
	if opts.UpdatedAfter != nil {
		vars["updated_after"] = opts.UpdatedAfter.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	m, err := a.call(ctx, "products", vars)
	if err != nil {
		return nil, err
	}
	items, err := array(m["items"])
	if err != nil {
		return nil, err
	}
	out := &upstream.ProductListResult{Items: make([]upstream.UpstreamProduct, 0, len(items))}
	seen := map[string]bool{}
	for _, item := range items {
		row, ok := object(item)
		if !ok {
			return nil, errors.New("invalid product object")
		}
		p, err := product(row)
		if err != nil {
			return nil, err
		}
		if seen[p.ExternalID] {
			return nil, errors.New("duplicate product ID")
		}
		seen[p.ExternalID] = true
		out.Items = append(out.Items, *p)
	}
	if v, ok := m["total"]; ok {
		out.Total, err = integer(v)
		if err != nil || out.Total < 0 {
			return nil, errors.New("invalid product total")
		}
		out.TotalKnown = true
	}
	if v, ok := m["has_more"]; ok {
		b, e := boolean(v)
		if e != nil {
			return nil, e
		}
		out.HasMore = &b
	}
	if v, ok := m["next_cursor"]; ok {
		out.NextCursor, err = identifier(v, true)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := m["includes_inactive"]; ok {
		out.IncludesInactive, err = boolean(v)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (a *adapter) GetProduct(ctx context.Context, id uint) (*upstream.UpstreamProduct, error) {
	return a.GetProductExternal(ctx, strconv.FormatUint(uint64(id), 10))
}
func (a *adapter) GetProductExternal(ctx context.Context, id string) (*upstream.UpstreamProduct, error) {
	if id == "" {
		return nil, errors.New("product ID is required")
	}
	m, err := a.call(ctx, "product", map[string]string{"product_id": id})
	if err != nil {
		return nil, err
	}
	return product(m)
}
func (a *adapter) CreateOrder(ctx context.Context, req upstream.CreateUpstreamOrderReq) (*upstream.CreateUpstreamOrderResp, error) {
	if req.Quantity <= 0 || req.DownstreamOrderNo == "" || req.ProductExternalID == "" {
		return nil, errors.New("product ID, positive quantity and downstream order number are required")
	}
	sku := req.SKUExternalID
	if sku == "" && req.SKUID != 0 {
		sku = strconv.FormatUint(uint64(req.SKUID), 10)
	}
	form, err := json.Marshal(req.ManualFormData)
	if err != nil {
		return nil, errors.New("invalid manual form data")
	}
	vars := map[string]string{"product_id": req.ProductExternalID, "sku_id": sku, "quantity": strconv.Itoa(req.Quantity), "downstream_order_no": req.DownstreamOrderNo, "trace_id": req.TraceID, "callback_url": req.CallbackURL, "manual_form_data": string(form)}
	// Exactly one attempt. Ambiguous failures must be reconciled, never replayed.
	m, err := a.call(ctx, "create_order", vars)
	if err != nil {
		return nil, err
	}
	detail, err := order(m)
	if err != nil {
		return nil, err
	}
	if _, canQuery := a.cfg.Endpoints["get_order"]; !canQuery && (detail.Fulfillment == nil || detail.Fulfillment.Status != "delivered") {
		return nil, errors.New("create_order did not deliver and order polling is unsupported")
	}
	return &upstream.CreateUpstreamOrderResp{OK: true, ExternalID: detail.ExternalID, OrderNo: detail.OrderNo, Status: detail.Status, Amount: detail.Amount, Currency: detail.Currency, Fulfillment: detail.Fulfillment}, nil
}
func (a *adapter) GetOrder(ctx context.Context, id uint) (*upstream.UpstreamOrderDetail, error) {
	return a.GetOrderExternal(ctx, strconv.FormatUint(uint64(id), 10))
}
func (a *adapter) GetOrderExternal(ctx context.Context, id string) (*upstream.UpstreamOrderDetail, error) {
	if id == "" {
		return nil, errors.New("order ID is required")
	}
	m, err := a.call(ctx, "get_order", map[string]string{"order_id": id})
	if err != nil {
		return nil, err
	}
	return order(m)
}
func (a *adapter) LookupOrder(ctx context.Context, no string) (*upstream.UpstreamOrderDetail, error) {
	if no == "" {
		return nil, errors.New("downstream order number is required")
	}
	m, err := a.call(ctx, "lookup_order", map[string]string{"downstream_order_no": no})
	if err != nil {
		return nil, err
	}
	return order(m)
}
func (a *adapter) CancelOrder(ctx context.Context, id uint) error {
	return a.CancelOrderExternal(ctx, strconv.FormatUint(uint64(id), 10))
}
func (a *adapter) CancelOrderExternal(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("order ID is required")
	}
	m, err := a.call(ctx, "cancel_order", map[string]string{"order_id": id})
	if err != nil {
		return err
	}
	status, err := orderStatus(m["status"])
	if err != nil {
		return err
	}
	if status != "canceled" {
		return upstream.ErrUpstreamCancelNotAllowed
	}
	return nil
}

func text(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", errors.New("expected string")
	}
	return s, nil
}
func boolean(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if x == "true" {
			return true, nil
		}
		if x == "false" {
			return false, nil
		}
	}
	return false, errors.New("expected strict boolean")
}
func object(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case jsonmap.JSON:
		return map[string]any(x), true
	default:
		return nil, false
	}
}
func array(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case []map[string]any:
		r := make([]any, len(x))
		for i := range x {
			r[i] = x[i]
		}
		return r, nil
	case []string:
		r := make([]any, len(x))
		for i := range x {
			r[i] = x[i]
		}
		return r, nil
	}
	return nil, errors.New("expected array")
}
func integer(v any) (int, error) {
	s, err := exactScalar(v)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("expected bounded integer")
	}
	return n, nil
}
func exactScalar(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return string(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	}
	return "", errors.New("expected exact scalar")
}

var numericID = regexp.MustCompile(`^-?[0-9]+$`)

func identifier(v any, empty bool) (string, error) {
	s, err := exactScalar(v)
	if err != nil {
		return "", errors.New("invalid external ID")
	}
	if _, ok := v.(json.Number); ok && !numericID.MatchString(s) {
		return "", errors.New("external numeric ID must be integral")
	}
	if (!empty && s == "") || len(s) > 4096 {
		return "", errors.New("invalid external ID")
	}
	return s, nil
}
func orderStatus(v any) (string, error) {
	s, err := text(v)
	if err != nil {
		return "", errors.New("order status is required")
	}
	switch s {
	case "pending_payment", "paid", "fulfilling", "partially_delivered", "partially_refunded", "delivered", "completed", "canceled", "refunded":
		return s, nil
	}
	return "", errors.New("unknown order status")
}
func currency(v any) (string, error) {
	s, err := text(v)
	if err != nil || len(s) != 3 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "", errors.New("currency must be a three-letter uppercase code")
	}
	return s, nil
}
