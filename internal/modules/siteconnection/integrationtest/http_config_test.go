package siteconnection_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/crypto"
	app "github.com/dujiao-next/internal/modules/siteconnection/application"
	contract "github.com/dujiao-next/internal/modules/siteconnection/contract"
	domain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/httpintegration"
	"github.com/dujiao-next/internal/upstream"
)

func httpTestService() (*app.Service, *siteConnectionRepoStub) {
	repo := &siteConnectionRepoStub{conn: &domain.Connection{ID: 1, Protocol: constants.ConnectionProtocolGenericHTTP, BaseURL: "https://example.com", Status: constants.ConnectionStatusPending}}
	return app.NewService(repo, "test-key", ""), repo
}
func TestHTTPConfigEncryptedRoundTrip(t *testing.T) {
	svc, repo := httpTestService()
	empty, err := svc.GetHTTPConfig(1)
	if err != nil || empty.Configured || len(empty.Config.Endpoints) != 0 {
		t.Fatalf("empty: %#v %v", empty, err)
	}
	input := app.HTTPConfigInput{Config: httpintegration.Config{Credentials: map[string]string{"key": "very-secret-key"}, Endpoints: map[string]httpintegration.Endpoint{"balance": {Method: "GET", Path: "/balance?access_token=url-secret", Headers: map[string]string{"Authorization": "Bearer bearer-secret"}, Query: map[string]string{"custom": "very-secret-key"}, Fields: map[string]httpintegration.Field{"balance": {Path: "balance"}}}}}}
	out, err := svc.PutHTTPConfig(1, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range out.Config.Credentials {
		if v != app.HTTPSecretMask {
			t.Fatal("unmasked credentials")
		}
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"very-secret-key", "bearer-secret", "url-secret"} {
		if strings.Contains(string(raw), secret) || strings.Contains(repo.conn.HTTPConfigEncrypted, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	plaintext, err := crypto.Decrypt(crypto.DeriveKey("test-key"), repo.conn.HTTPConfigEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	var stored httpintegration.Config
	if json.Unmarshal([]byte(plaintext), &stored) != nil {
		t.Fatal("not encrypted JSON")
	}
	if stored.Endpoints["balance"].Headers["Authorization"] == "Bearer bearer-secret" || stored.Endpoints["balance"].Query["custom"] != "{{credential_key}}" {
		t.Fatal("inline credentials not extracted")
	}
	got, err := svc.GetHTTPConfig(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: got.Config, Revision: got.Revision}); err != nil {
		t.Fatal(err)
	}
	plaintext, _ = crypto.Decrypt(crypto.DeriveKey("test-key"), repo.conn.HTTPConfigEncrypted)
	if !strings.Contains(plaintext, "very-secret-key") || !strings.Contains(plaintext, "bearer-secret") {
		t.Fatal("masked roundtrip lost secrets")
	}
	serialized, _ := json.Marshal(repo.conn)
	if strings.Contains(string(serialized), "http_config_encrypted") || strings.Contains(string(serialized), repo.conn.HTTPConfigEncrypted) {
		t.Fatal("CRUD exposes config")
	}
}
func TestHTTPActivationAndFrozenConfiguration(t *testing.T) {
	svc, repo := httpTestService()
	var used httpintegration.Config
	svc.SetHTTPAdapter(func(_ string, cfg httpintegration.Config) (upstream.Adapter, error) { used = cfg; return nil, nil }, func(cfg httpintegration.Config) error {
		if _, ok := cfg.Endpoints["products"]; !ok {
			return errors.New("missing products")
		}
		return nil
	})
	cfg := httpintegration.Config{Credentials: map[string]string{"key": "first-secret"}, Endpoints: map[string]httpintegration.Endpoint{"products": {Path: "/products"}}}
	out, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: cfg})
	if err != nil || !out.Ready {
		t.Fatalf("ready configuration: %#v %v", out, err)
	}
	if err := svc.SetStatus(1, constants.ConnectionStatusActive); err != nil {
		t.Fatal(err)
	}
	frozen, err := svc.FreezeHTTPConnection(repo.conn)
	if err != nil || strings.Contains(frozen, "first-secret") {
		t.Fatalf("snapshot: %v", err)
	}
	cfg.Credentials["key"] = "second-secret"
	if _, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: cfg, Revision: out.Revision}); err != nil {
		t.Fatal(err)
	}
	if repo.conn.Status != constants.ConnectionStatusDisabled {
		t.Fatal("configuration change left connection active")
	}
	conn, _, err := svc.RestoreHTTPConnection(1, frozen)
	if err != nil || conn.HTTPConfigRevision != 1 || used.Credentials["key"] != "first-secret" {
		t.Fatalf("restore: %#v %v", conn, err)
	}
	if _, _, err := svc.RestoreHTTPConnection(2, frozen); err == nil {
		t.Fatal("snapshot accepted for another connection")
	}
	if _, _, err := svc.RestoreHTTPConnection(1, frozen+"x"); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestHTTPConfigRevisionAndExplicitDeletion(t *testing.T) {
	svc, repo := httpTestService()
	cfg := httpintegration.Config{Credentials: map[string]string{"token": "secret-123"}, Endpoints: map[string]httpintegration.Endpoint{
		"balance": {Path: "/balance", Headers: map[string]string{"Authorization": "{{credential_token}}"}},
	}}
	first, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: cfg})
	if err != nil || first.Revision != 1 {
		t.Fatalf("first save: %#v %v", first, err)
	}
	before := repo.conn.HTTPConfigEncrypted
	if _, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: first.Config}); !errors.Is(err, contract.ErrHTTPConfigConflict) {
		t.Fatalf("stale save accepted: %v", err)
	}
	if repo.conn.HTTPConfigEncrypted != before {
		t.Fatal("stale save changed ciphertext")
	}
	if _, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: first.Config, Revision: 1, DeleteCredentials: []string{"token"}}); !errors.Is(err, contract.ErrHTTPConfigInvalid) {
		t.Fatalf("deleted referenced credential: %v", err)
	}
	out, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: first.Config, Revision: 1, DeleteCredentials: []string{"token"}, DeleteEndpoints: []string{"balance"}})
	if err != nil || out.Revision != 2 || len(out.Config.Credentials) != 0 || len(out.Config.Endpoints) != 0 {
		t.Fatalf("delete: %#v %v", out, err)
	}
	if repo.conn.Status != constants.ConnectionStatusDisabled {
		t.Fatal("config change must invalidate activation")
	}
}

func TestHTTPCurlBasePathSurvivesSaveRoundTrip(t *testing.T) {
	for _, base := range []string{"https://example.com/api", "https://example.com/api/"} {
		svc, repo := httpTestService()
		repo.conn.BaseURL = base
		result, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "parse_curl", Endpoint: "create_order", Curl: `curl 'https://example.com/api/orders?lang=en' -H 'Content-Type: application/json' -d '{"quantity":2,"enabled":true,"product_id":"001"}'`})
		if err != nil {
			t.Fatal(err)
		}
		spec := result.Request
		if spec.Path != "/api/orders" || spec.Query["lang"] != "en" {
			t.Fatalf("import path/query: %#v", spec)
		}
		out, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: httpintegration.Config{Endpoints: map[string]httpintegration.Endpoint{"create_order": {Method: spec.Method, Path: spec.Path, Headers: spec.Headers, Query: spec.Query, Encoding: spec.Encoding, Body: spec.Body, JSONBody: spec.JSONBody}}}})
		if err != nil {
			t.Fatal(err)
		}
		ep := out.Config.Endpoints["create_order"]
		if ep.Path != "/api/orders" || ep.Query["lang"] != "en" || !strings.Contains(string(ep.JSONBody), `"product_id":"001"`) || !strings.Contains(string(ep.JSONBody), `"quantity":2`) {
			t.Fatalf("saved import: %#v", ep)
		}
	}
}

func TestHTTPCurlImportChecksConnectionOrigin(t *testing.T) {
	svc, _ := httpTestService()
	for _, value := range []string{"curl https://other.example/balance", "curl http://example.com/balance", "curl https://example.com:444/balance"} {
		if _, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "parse_curl", Endpoint: "balance", Curl: value}); !errors.Is(err, contract.ErrHTTPConfigInvalid) {
			t.Fatalf("cross origin accepted: %s %v", value, err)
		}
	}
	out, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "parse_curl", Endpoint: "balance", Curl: "curl 'https://example.com/balance?page=1'"})
	if err != nil || out.Request == nil || out.Request.Path != "/balance" || out.Request.Query["page"] != "1" {
		t.Fatalf("relative import: %#v %v", out, err)
	}
}

func TestHTTPDraftAndProtocolGuards(t *testing.T) {
	svc, repo := httpTestService()
	if err := svc.SetStatus(1, constants.ConnectionStatusActive); !errors.Is(err, contract.ErrHTTPDraftOnly) {
		t.Fatal(err)
	}
	if _, err := svc.Ping(1); !errors.Is(err, contract.ErrHTTPDraftOnly) {
		t.Fatal(err)
	}
	if _, err := svc.GetAdapter(repo.conn); !errors.Is(err, contract.ErrHTTPDraftOnly) {
		t.Fatal(err)
	}
	if _, err := svc.Update(1, app.UpdateInput{Protocol: constants.ConnectionProtocolDujiaoNext}); !errors.Is(err, contract.ErrProtocolImmutable) {
		t.Fatal(err)
	}
	if repo.updated {
		t.Fatal("guard wrote connection")
	}
	if _, err := svc.Create(app.CreateInput{Name: "draft", BaseURL: "https://example.com", Protocol: constants.ConnectionProtocolGenericHTTP}); err != nil {
		t.Fatal(err)
	}
	if repo.conn.Status != constants.ConnectionStatusPending {
		t.Fatal("draft activated")
	}
	if _, err := svc.Create(app.CreateInput{Name: "unknown", BaseURL: "https://example.com", Protocol: "unknown"}); !errors.Is(err, contract.ErrProtocolUnsupported) {
		t.Fatal(err)
	}
	if _, err := svc.Create(app.CreateInput{Name: "legacy", BaseURL: "https://example.com"}); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("legacy lost key validation")
	}
}
func TestHTTPDebugOfflineAndReadOnlyOperations(t *testing.T) {
	svc, repo := httpTestService()
	cfg := httpintegration.Config{Credentials: map[string]string{"token": "sensitive-value"}, Endpoints: map[string]httpintegration.Endpoint{
		"balance": {Path: "/balance", Fields: map[string]httpintegration.Field{"balance": {Path: "balance"}}},
		"create":  {Method: "GET", Path: "/create"},
	}}
	if _, err := svc.PutHTTPConfig(1, app.HTTPConfigInput{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"create", "cancel", "unknown"} {
		if _, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "test", Endpoint: operation}); !errors.Is(err, contract.ErrHTTPConfigInvalid) {
			t.Fatal(operation, err)
		}
	}
	out, err := svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "map_response", Endpoint: "balance", Response: `{"balance":"sensitive-value"}`})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mapped["balance"] != "[REDACTED]" {
		t.Fatalf("mapping leaked: %#v", out)
	}
	out, err = svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "parse_curl", Endpoint: "balance", Curl: "curl https://example.com/balance"})
	if err != nil || out.Request == nil {
		t.Fatalf("parse: %#v %v", out, err)
	}
	if _, err = svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "parse_curl", Endpoint: "balance", Curl: strings.Repeat("x", (32<<10)+1)}); !errors.Is(err, contract.ErrHTTPConfigInvalid) {
		t.Fatal(err)
	}
	// A reserved address fails before any outbound request and returns a fixed error.
	repo.conn.BaseURL = "http://127.0.0.1"
	if _, err = svc.HTTPDebug(context.Background(), 1, app.HTTPDebugInput{Action: "test", Endpoint: "balance"}); !errors.Is(err, contract.ErrHTTPDebugFailed) {
		t.Fatal(err)
	}
	if repo.conn.Status == constants.ConnectionStatusActive {
		t.Fatal("debug activated connection")
	}
}
