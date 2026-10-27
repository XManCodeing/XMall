package generichttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/shared/httpintegration"
	"github.com/dujiao-next/internal/upstream"
	"github.com/shopspring/decimal"
)

func fixtureConfig(t *testing.T) httpintegration.Config {
	t.Helper()
	const raw = `{"version":1,"credentials":{"token":"literal-secret"},"endpoints":{
 "products":{"path":"/products","list_path":"data","fields":{"id":{"path":"id"},"title":{"path":"title"},"price_amount":{"path":"price_amount"},"currency":{"path":"currency"},"stock_quantity":{"path":"stock_quantity"},"is_active":{"path":"is_active"},"skus":{"path":"skus"}},"meta_fields":{"total":{"path":"total"},"has_more":{"path":"has_more"},"next_cursor":{"path":"next_cursor"}}},
 "product":{"path":"/products/{{product_id}}","fields":{"id":{"path":"id"},"title":{"path":"title"},"price_amount":{"path":"price_amount"},"currency":{"path":"currency"},"stock_quantity":{"path":"stock_quantity"},"is_active":{"path":"is_active"},"skus":{"path":"skus"}}},
 "create_order":{"method":"POST","path":"/orders","encoding":"json","headers":{"Authorization":"Bearer {{credential_token}}"},"body":{"product":"{{product_id}}","sku":"{{sku_id}}","quantity":"{{quantity}}","reference":"{{downstream_order_no}}"},"fields":{"order_id":{"path":"order_id"},"order_no":{"path":"order_no"},"status":{"path":"status"},"amount":{"path":"amount"},"currency":{"path":"currency"},"fulfillment":{"path":"fulfillment"},"cards":{"path":"cards"},"payload":{"path":"payload"}}},
 "get_order":{"path":"/orders/{{order_id}}","fields":{"order_id":{"path":"order_id"},"order_no":{"path":"order_no"},"status":{"path":"status"},"amount":{"path":"amount"},"currency":{"path":"currency"},"fulfillment":{"path":"fulfillment"},"cards":{"path":"cards"},"payload":{"path":"payload"}}},
 "lookup_order":{"path":"/lookup","query":{"reference":"{{downstream_order_no}}"},"fields":{"order_id":{"path":"order_id"},"status":{"path":"status"},"amount":{"path":"amount"},"currency":{"path":"currency"},"payload":{"path":"payload"}}},
 "cancel_order":{"method":"POST","path":"/orders/{{order_id}}/cancel","fields":{"status":{"path":"status"}}}
 }}`
	var cfg httpintegration.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}
func mockAdapter(t *testing.T, cfg httpintegration.Config, body string, check func(httpintegration.Endpoint, map[string]string)) *adapter {
	t.Helper()
	base, err := New("https://supplier.example", cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := base.(*adapter)
	a.execute = func(_ context.Context, _ string, ep httpintegration.Endpoint, vars map[string]string) (*httpintegration.Result, error) {
		if check != nil {
			check(ep, vars)
		}
		mapped, err := httpintegration.MapResponse(ep, []byte(body))
		return &httpintegration.Result{StatusCode: 200, Mapped: mapped}, err
	}
	return a
}

const single = `{"id":" 0001/中文 ","title":"商品","price_amount":1.2300,"currency":"CNY","stock_quantity":-1,"is_active":true}`
const asynchronous = `{"order_id":" 00000000000000000001/X ","order_no":"00099","status":"paid","amount":1.23,"currency":"CNY"}`

func TestJSONLikeIdentifiersAndCredentialsRemainOpaque(t *testing.T) {
	for _, raw := range []string{"123", "true", "null", "001"} {
		t.Run(raw, func(t *testing.T) {
			cfg := fixtureConfig(t)
			cfg.Credentials = map[string]string{"token": raw, "quantity": raw}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var restored httpintegration.Config
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"order_id":%q,"status":"paid","amount":"1","currency":"USD"}`, raw)
			a := mockAdapter(t, restored, body, func(_ httpintegration.Endpoint, vars map[string]string) {
				for _, key := range []string{"product_id", "sku_id", "downstream_order_no", "credential_token", "credential_quantity"} {
					if vars[key] != raw {
						t.Fatalf("opaque %s changed: %q", key, vars[key])
					}
				}
				if vars["quantity"] != "2" {
					t.Fatal("credential quantity collided with system quantity")
				}
			})
			result, err := a.CreateOrder(context.Background(), upstream.CreateUpstreamOrderReq{ProductExternalID: raw, SKUExternalID: raw, DownstreamOrderNo: raw, Quantity: 2})
			if err != nil || result.ExternalID != raw || result.OrderID != 0 {
				t.Fatalf("opaque order result: %+v %v", result, err)
			}
		})
	}
}

func TestSingleProductAndOpaqueIDs(t *testing.T) {
	cfg := fixtureConfig(t)
	id := " 0001/中文 "
	a := mockAdapter(t, cfg, single, func(_ httpintegration.Endpoint, v map[string]string) {
		if v["product_id"] != id || v["credential_token"] != "literal-secret" {
			t.Fatalf("variables %#v", v)
		}
	})
	p, err := a.GetProductExternal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 0 || p.ExternalID != id || p.Title["zh-CN"] != "商品" || p.PriceAmount != "1.23" || len(p.SKUs) != 1 {
		t.Fatalf("product %#v", p)
	}
	sku := p.SKUs[0]
	if sku.ID != 0 || sku.ExternalID != "" || !sku.ProductLevel || sku.StockQuantity != -1 || sku.StockStatus != "unlimited" {
		t.Fatalf("SKU %#v", sku)
	}
}
func TestListPaginationAndExactNumbers(t *testing.T) {
	cfg := fixtureConfig(t)
	body := `{"data":[{"id":18446744073709551616001,"title":"Number","price_amount":0.000000000000000001,"currency":"USD","stock_quantity":0,"is_active":false}],"total":300,"has_more":false,"next_cursor":" 000next "}`
	a := mockAdapter(t, cfg, body, func(_ httpintegration.Endpoint, v map[string]string) {
		for k, want := range map[string]string{"page": "2", "page_size": "10", "offset": "10", "cursor": " 000old "} {
			if v[k] != want {
				t.Fatalf("%s=%q", k, v[k])
			}
		}
	})
	r, err := a.ListProducts(context.Background(), upstream.ListProductsOpts{Page: 2, PageSize: 10, Offset: 10, Cursor: " 000old "})
	if err != nil {
		t.Fatal(err)
	}
	if !r.TotalKnown || r.Total != 300 || r.HasMore == nil || *r.HasMore || r.NextCursor != " 000next " || r.Items[0].ExternalID != "18446744073709551616001" || r.Items[0].PriceAmount != "0.000000000000000001" {
		t.Fatalf("result %#v", r)
	}
	a = mockAdapter(t, cfg, `{"data":[]}`, nil)
	r, err = a.ListProducts(context.Background(), upstream.ListProductsOpts{})
	if err != nil || r.TotalKnown || r.HasMore != nil || r.NextCursor != "" {
		t.Fatalf("unknown metadata %#v %v", r, err)
	}
}
func TestMultiSKUAndTypedJSONArray(t *testing.T) {
	cfg := fixtureConfig(t)
	data := `{"id":"P","title":{"zh-CN":"多规格"},"currency":"USD","is_active":true,"skus":[{"id":"a","price_amount":"2","stock_quantity":4},{"id":"b","sku_code":"generic:61","price_amount":"1","stock_quantity":0},{"id":"00/a","price_amount":"3","stock_quantity":-1}]}`
	a := mockAdapter(t, cfg, data, nil)
	p, err := a.GetProductExternal(context.Background(), "P")
	if err != nil {
		t.Fatal(err)
	}
	if p.PriceAmount != "1" || len(p.SKUs) != 3 || p.SKUs[0].SKUCode != "generic:61:" || p.SKUs[0].ProductLevel || p.SKUs[0].ExternalID != "a" {
		t.Fatalf("result %#v", p)
	}
	first := p.SKUs
	mapped, err := httpintegration.MapResponse(cfg.Endpoints["product"], []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	rows := mapped["skus"].([]any)
	typed := []map[string]any{rows[2].(map[string]any), rows[1].(map[string]any), rows[0].(map[string]any)}
	mapped["skus"] = typed
	a.execute = func(context.Context, string, httpintegration.Endpoint, map[string]string) (*httpintegration.Result, error) {
		return &httpintegration.Result{StatusCode: 200, Mapped: mapped}, nil
	}
	p, err = a.GetProductExternal(context.Background(), "P")
	if err != nil {
		t.Fatal(err)
	}
	if p.SKUs[2].SKUCode != first[0].SKUCode || p.SKUs[0].SKUCode != first[2].SKUCode {
		t.Fatal("codes depend on ordering")
	}
	for _, mutate := range []func(){func() { typed[1]["id"] = "a" }, func() { typed[1]["id"] = "b"; typed[2]["sku_code"] = "generic:61" }, func() { typed[2]["sku_code"] = ""; typed[2]["stock_quantity"] = json.Number("1.1") }} {
		mutate()
		if _, err = a.GetProductExternal(context.Background(), "P"); err == nil {
			t.Fatal("invalid SKU accepted")
		}
	}
}
func TestCreateSyncAsyncLookupAndCancel(t *testing.T) {
	cfg := fixtureConfig(t)
	req := upstream.CreateUpstreamOrderReq{ProductExternalID: " 0001/P ", SKUExternalID: "00/S", Quantity: 2, DownstreamOrderNo: "000down", TraceID: "trace"}
	calls := 0
	a := mockAdapter(t, cfg, asynchronous, func(_ httpintegration.Endpoint, v map[string]string) {
		calls++
		if v["product_id"] != req.ProductExternalID || v["sku_id"] != req.SKUExternalID || v["downstream_order_no"] != req.DownstreamOrderNo || v["quantity"] != "2" {
			t.Fatalf("bad variables %#v", v)
		}
	})
	r, err := a.CreateOrder(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.Status != "paid" || r.Fulfillment != nil || r.OrderID != 0 || r.ExternalID != " 00000000000000000001/X " || calls != 1 {
		t.Fatalf("create %#v", r)
	}
	for _, delivery := range []string{`"cards":["literal-secret"," 000-card "]`, `"payload":"literal-secret\n 000-card "`, `"fulfillment":{"status":"delivered","type":"auto","cards":["literal-secret"," 000-card "]}`} {
		body := `{"order_id":900719925474099300001,"status":"delivered","amount":"1.23","currency":"CNY",` + delivery + `}`
		a = mockAdapter(t, cfg, body, nil)
		r, err = a.CreateOrder(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if r.ExternalID != "900719925474099300001" || r.Fulfillment == nil || r.Fulfillment.Payload != "literal-secret\n 000-card " || r.Fulfillment.Status != "delivered" {
			t.Fatalf("sync %#v", r)
		}
	}
	a = mockAdapter(t, cfg, asynchronous, func(_ httpintegration.Endpoint, v map[string]string) {
		if v["downstream_order_no"] != " 000lookup " {
			t.Fatal("lookup key changed")
		}
	})
	if _, err = a.LookupOrder(context.Background(), " 000lookup "); err != nil {
		t.Fatal(err)
	}
	a = mockAdapter(t, cfg, asynchronous, func(_ httpintegration.Endpoint, v map[string]string) {
		if v["order_id"] != " 000/O " {
			t.Fatal("order key changed")
		}
	})
	if _, err = a.GetOrderExternal(context.Background(), " 000/O "); err != nil {
		t.Fatal(err)
	}
	a = mockAdapter(t, cfg, `{"status":"canceled"}`, nil)
	if err = a.CancelOrderExternal(context.Background(), "O"); err != nil {
		t.Fatal(err)
	}
	a = mockAdapter(t, cfg, `{"status":"paid"}`, nil)
	if !errors.Is(a.CancelOrderExternal(context.Background(), "O"), upstream.ErrUpstreamCancelNotAllowed) {
		t.Fatal("cancel falsely succeeded")
	}
}
func TestFailuresNeverRetryOrLeak(t *testing.T) {
	cfg := fixtureConfig(t)
	base, err := New("https://supplier.example", cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := base.(*adapter)
	calls := 0
	a.execute = func(context.Context, string, httpintegration.Endpoint, map[string]string) (*httpintegration.Result, error) {
		calls++
		return &httpintegration.Result{StatusCode: 500, Body: "literal-secret"}, errors.New("literal-secret")
	}
	_, err = a.CreateOrder(context.Background(), upstream.CreateUpstreamOrderReq{ProductExternalID: "P", DownstreamOrderNo: "D", Quantity: 1})
	if err == nil || strings.Contains(err.Error(), "literal-secret") || calls != 1 {
		t.Fatalf("failure %v calls=%d", err, calls)
	}
	_, err = a.call(context.Background(), "product", map[string]string{"credential_token": "override"})
	if err == nil || calls != 1 {
		t.Fatal("credential override reached executor")
	}
	if _, err = a.DownloadImage(context.Background(), "http://127.0.0.1/private"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("image download exposed")
	}
	delete(a.cfg.Endpoints, "lookup_order")
	if _, err = a.LookupOrder(context.Background(), "D"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported lookup not explicit")
	}
	for _, status := range []string{"", "success", "shipped", "unknown", "DELIVERED"} {
		m := map[string]any{"order_id": "O", "status": status, "amount": "1", "currency": "CNY"}
		if _, err = order(m); err == nil {
			t.Fatalf("accepted status %q", status)
		}
	}
	for _, body := range []string{`{"order_id":"O","status":"delivered","amount":1,"currency":"CNY"}`, `{"order_id":"O","status":"paid","amount":-1,"currency":"CNY"}`, `{"order_id":"O","status":"paid","amount":1,"currency":"cny"}`, `{"order_id":"O","status":"delivered","amount":1,"currency":"CNY","cards":[{"secret":"x"}]}`} {
		a = mockAdapter(t, cfg, body, nil)
		if _, err = a.GetOrderExternal(context.Background(), "O"); err == nil {
			t.Fatalf("accepted malformed %s", body)
		}
	}
}
func TestExactScalarAndStrictValues(t *testing.T) {
	for _, v := range []any{"0001", " A/中文 ", json.Number("18446744073709551616000"), int64(123)} {
		id, err := identifier(v, false)
		if err != nil || id != fmt.Sprint(v) {
			t.Fatalf("ID %v -> %q %v", v, id, err)
		}
	}
	for _, v := range []any{float64(1), json.Number("1e3"), json.Number("1.0"), true, nil, ""} {
		if _, err := identifier(v, false); err == nil {
			t.Fatalf("accepted ID %#v", v)
		}
	}
	for _, v := range []any{float64(1), "NaN", "-1", "1e999", "0.0000000000000000001", " 1", true} {
		if _, err := money(v); err == nil {
			t.Fatalf("accepted money %#v", v)
		}
	}
	d := decimal.RequireFromString("123456789.123456789")
	if s, err := money(d); err != nil || s != "123456789.123456789" {
		t.Fatal(s, err)
	}
	for _, v := range []any{1, "1", "TRUE", nil} {
		if _, err := boolean(v); err == nil {
			t.Fatalf("accepted boolean %#v", v)
		}
	}
}
func TestPingBalanceAndLegacyMethods(t *testing.T) {
	cfg := fixtureConfig(t)
	cfg.Endpoints["ping"] = httpintegration.Endpoint{Path: "/ping", Fields: map[string]httpintegration.Field{"site_name": {Path: "site"}}}
	cfg.Endpoints["balance"] = httpintegration.Endpoint{Path: "/balance", Fields: map[string]httpintegration.Field{"balance": {Path: "balance"}, "currency": {Path: "currency"}}}
	a := mockAdapter(t, cfg, `{"site":"Mock","balance":123456789.123456789,"currency":"USD"}`, nil)
	r, err := a.Ping(context.Background())
	if err != nil || r.SiteName != "Mock" || r.Balance != "123456789.123456789" {
		t.Fatalf("ping %#v %v", r, err)
	}
	delete(a.cfg.Endpoints, "ping")
	if _, err = a.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(a.cfg.Endpoints, "balance")
	if _, err = a.Ping(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	categories, err := a.ListCategories(context.Background())
	if err != nil || categories.Supported {
		t.Fatal(categories, err)
	}
	a = mockAdapter(t, cfg, single, func(_ httpintegration.Endpoint, v map[string]string) {
		if v["product_id"] != "123" {
			t.Fatal(v)
		}
	})
	if _, err = a.GetProduct(context.Background(), 123); err != nil {
		t.Fatal(err)
	}
	a = mockAdapter(t, cfg, asynchronous, func(_ httpintegration.Endpoint, v map[string]string) {
		if v["order_id"] != "123" {
			t.Fatal(v)
		}
	})
	if _, err = a.GetOrder(context.Background(), 123); err != nil {
		t.Fatal(err)
	}
	a = mockAdapter(t, cfg, `{"status":"canceled"}`, nil)
	if err = a.CancelOrder(context.Background(), 123); err != nil {
		t.Fatal(err)
	}
}
func TestCollectionMappingAndEnum(t *testing.T) {
	cfg := fixtureConfig(t)
	ep := cfg.Endpoints["product"]
	delete(ep.Fields, "skus")
	ep.Collections = map[string]httpintegration.Collection{"skus": {Path: "variants", Fields: map[string]httpintegration.Field{"id": {Path: "key"}, "price_amount": {Path: "cents", Transform: "cents_to_units"}, "stock_quantity": {Path: "qty", Transform: "int"}, "is_active": {Path: "enabled", Enum: map[string]string{"yes": "true", "no": "false"}, Transform: "bool"}}}}
	cfg.Endpoints["product"] = ep
	body := `{"id":"P","title":"Mapped","currency":"USD","is_active":true,"variants":[{"key":"000/01","cents":123,"qty":2,"enabled":"yes"}]}`
	a := mockAdapter(t, cfg, body, nil)
	p, err := a.GetProductExternal(context.Background(), "P")
	if err != nil || p.SKUs[0].ExternalID != "000/01" || p.SKUs[0].PriceAmount != "1.23" {
		t.Fatal(p, err)
	}
	a = mockAdapter(t, cfg, strings.ReplaceAll(body, `"yes"`, `"maybe"`), nil)
	if _, err = a.GetProductExternal(context.Background(), "P"); err == nil {
		t.Fatal("unknown enum accepted")
	}
	orderEP := cfg.Endpoints["get_order"]
	field := orderEP.Fields["status"]
	field.Enum = map[string]string{"done": "delivered", "working": "fulfilling"}
	orderEP.Fields["status"] = field
	cfg.Endpoints["get_order"] = orderEP
	a = mockAdapter(t, cfg, `{"order_id":"O","status":"done","amount":1,"currency":"USD","cards":["secret"]}`, nil)
	detail, err := a.GetOrderExternal(context.Background(), "O")
	if err != nil || detail.Status != "delivered" || detail.Fulfillment.Payload != "secret" {
		t.Fatal(detail, err)
	}
}
func TestSynchronousOnlyMustActuallyDeliver(t *testing.T) {
	cfg := fixtureConfig(t)
	delete(cfg.Endpoints, "get_order")
	a := mockAdapter(t, cfg, asynchronous, nil)
	req := upstream.CreateUpstreamOrderReq{ProductExternalID: "P", Quantity: 1, DownstreamOrderNo: "D"}
	if _, err := a.CreateOrder(context.Background(), req); err == nil {
		t.Fatal("async result accepted without polling")
	}
	a = mockAdapter(t, cfg, `{"order_id":"O","status":"paid","amount":1,"currency":"CNY","fulfillment":{"status":"delivered","payload":" secret "}}`, nil)
	r, err := a.CreateOrder(context.Background(), req)
	if err != nil || r.Fulfillment.Payload != " secret " || r.Fulfillment.Status != "delivered" {
		t.Fatal(r, err)
	}
}

func TestValidationCapabilitiesAndConfigOwnership(t *testing.T) {
	cfg := fixtureConfig(t)
	caps := Capabilities(cfg)
	if !caps.Products || !caps.Product || !caps.CreateOrder || !caps.GetOrder || !caps.LookupOrder || !caps.CancelOrder || !caps.SynchronousFulfillment || caps.Ping {
		t.Fatalf("caps %#v", caps)
	}
	base, err := New("https://supplier.example", cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := base.(*adapter)
	cfg.Credentials["token"] = "changed"
	delete(cfg.Endpoints, "lookup_order")
	if a.cfg.Credentials["token"] != "literal-secret" || !Capabilities(a.cfg).LookupOrder {
		t.Fatal("caller mutation changed adapter")
	}
	for _, mutate := range []func(*httpintegration.Config){func(c *httpintegration.Config) { c.Version = 0 }, func(c *httpintegration.Config) { delete(c.Endpoints, "products") }, func(c *httpintegration.Config) { delete(c.Endpoints, "product") }, func(c *httpintegration.Config) { delete(c.Endpoints, "create_order") }, func(c *httpintegration.Config) {
		delete(c.Endpoints, "get_order")
		ep := c.Endpoints["create_order"]
		delete(ep.Fields, "fulfillment")
		delete(ep.Fields, "payload")
		delete(ep.Fields, "cards")
		c.Endpoints["create_order"] = ep
	}, func(c *httpintegration.Config) { c.Credentials["bad-key"] = "x" }} {
		c := fixtureConfig(t)
		mutate(&c)
		if err := Validate(c); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	for _, url := range []string{"file:///a", "https://u:p@example.com", "https://example.com?x=secret", "https://example.com#secret"} {
		if _, err := New(url, fixtureConfig(t)); err == nil {
			t.Fatal("bad URL accepted")
		}
	}
	// Native numeric wrappers and typed object slices are accepted without JSON roundtrips.
	got, err := array([]map[string]any{{"id": "x"}})
	if err != nil || !reflect.DeepEqual(got, []any{map[string]any{"id": "x"}}) {
		t.Fatal(got, err)
	}
}
