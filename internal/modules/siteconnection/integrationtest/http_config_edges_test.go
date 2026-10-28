package siteconnection_test

import (
	"context"
	"encoding/json"
	"github.com/homles698698-lab/XMall/internal/crypto"
	"strings"
	"testing"

	app "github.com/homles698698-lab/XMall/internal/modules/siteconnection/application"
	"github.com/homles698698-lab/XMall/internal/shared/httpintegration"
)

func TestHTTPConfigTypedBodySecretsAreExtracted(t *testing.T) {
	svc, repo := httpTestService()
	cfg := httpintegration.Config{Version: 1, Endpoints: map[string]httpintegration.Endpoint{
		"create_order": {Method: "POST", Path: "/orders", Encoding: "json", JSONBody: json.RawMessage(`{"quantity":2,"nested":{"api_key":"body-secret","enabled":false},"form":{"email":"{{email}}"}}`)},
	}}
	out, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "body-secret") || strings.Contains(repo.conn.HTTPConfigEncrypted, "body-secret") {
		t.Fatal("typed body credential leaked")
	}
	if !strings.Contains(string(out.Config.Endpoints["create_order"].JSONBody), `"quantity":2`) || !strings.Contains(string(out.Config.Endpoints["create_order"].JSONBody), `"enabled":false`) {
		t.Fatal("typed body values changed")
	}
	if _, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: out.Config, Revision: out.Revision}); err != nil {
		t.Fatal(err)
	}
	plaintext, err := crypto.Decrypt(crypto.DeriveKey("test-key"), repo.conn.HTTPConfigEncrypted)
	if err != nil || !strings.Contains(plaintext, "body-secret") {
		t.Fatal("typed body mask roundtrip lost secret")
	}
}

func TestHTTPConfigMixedAuthorizationDoesNotExposeLiteralSecret(t *testing.T) {
	svc, _ := httpTestService()
	out, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: httpintegration.Config{
		Endpoints: map[string]httpintegration.Endpoint{
			"balance": {Path: "/balance", Headers: map[string]string{
				"Authorization": "Bearer literal-sensitive-token; nonce={{nonce}}",
			}},
		},
	}})
	if err != nil {
		// Unsupported mixed secrets may be rejected, but must not be echoed.
		return
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "literal-sensitive-token") {
		t.Fatal("configuration readback exposes literal authorization secret alongside template")
	}
}

func TestHTTPMappingDoesNotRedactOrdinaryPaginationVariables(t *testing.T) {
	svc, _ := httpTestService()
	_, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: httpintegration.Config{
		Endpoints: map[string]httpintegration.Endpoint{
			"products": {Path: "/products", Fields: map[string]httpintegration.Field{
				"id":    {Path: "id", Transform: "string"},
				"price": {Path: "price", Transform: "decimal"},
			}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{
		Action: "map_response", Endpoint: "products", Response: `{"id":"A123","price":"10.50"}`,
		Variables: map[string]string{"page": "1", "page_size": "10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("ordinary request variables corrupt mapped product identifiers or prices")
	}
}
