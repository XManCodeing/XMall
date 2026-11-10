package container

import (
	"context"
	"errors"
	"github.com/XManCodeing/XMall/internal/authz"
	"github.com/XManCodeing/XMall/internal/config"
	admincontract "github.com/XManCodeing/XMall/internal/modules/identity/admin/contract"
	admindomain "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"

	"github.com/XManCodeing/XMall/internal/constants"
)

type supportRuntimeFixture struct {
	r.Store
	instance *rd.Instance
}

func (f supportRuntimeFixture) Get(context.Context, uint) (*rd.Instance, error) {
	return f.instance, nil
}

// TestSupportAdminGeneration rejects stopped/external runtimes and reads the authoritative generation.
func TestSupportAdminGeneration(t *testing.T) {
	instance := &rd.Instance{ID: 2, Enabled: true, Mode: "internal", State: "running", Generation: 23}
	container := &Container{TelegramBotStore: supportRuntimeFixture{instance: instance}}
	generation, err := container.supportAdminGeneration(context.Background(), 2)
	if err != nil || generation != 23 {
		t.Fatal(generation, err)
	}
	instance.State = "stopped"
	if _, err = container.supportAdminGeneration(context.Background(), 2); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	instance.State = "running"
	instance.Mode = "external"
	if _, err = container.supportAdminGeneration(context.Background(), 2); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
}

// supportAdminIdentityStore deliberately exposes only backend identities to the adapter.
type supportAdminIdentityStore struct {
	admincontract.Store
	admins map[uint]*admindomain.Admin
}

func (s supportAdminIdentityStore) GetByID(id uint) (*admindomain.Admin, error) {
	return s.admins[id], nil
}

// TestSupportAdminExactContentPermission rejects wildcard-only grants and unrelated built-in roles.
func TestSupportAdminExactContentPermission(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "authz.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	auth, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	if err = auth.GrantRolePolicy("wildcard", "/admin/*", "*"); err != nil {
		t.Fatal(err)
	}
	identities := supportAdminIdentityStore{admins: map[uint]*admindomain.Admin{}}
	container := &Container{AdminStore: identities, AuthzService: auth, TelegramBotStore: supportRuntimeFixture{instance: &rd.Instance{ID: 1}}}
	for index, role := range []string{"support", "system_admin", "readonly_auditor", "wildcard"} {
		id := uint(index + 1)
		identities.admins[id] = &admindomain.Admin{ID: id}
		if err = auth.SetAdminRoles(id, []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"GET", "PUT"} {
			err = container.authorizeSupportSettings(context.Background(), id, 1, method)
			if (err == nil) != (role == "system_admin") {
				t.Fatal("settings", role, method, err)
			}
		}
		err = container.AuthorizeSupportTopicDeletion(context.Background(), id, 1)
		if (err == nil) != (role == "support") {
			t.Fatal("topic deletion exact POST", role, err)
		}
		err = container.AuthorizeSupportResolution(context.Background(), id, 1)
		if (err == nil) != (role == "support") {
			t.Fatal("resolution", role, err)
		}
		err = container.authorizeSupportAdmin(context.Background(), id, c.AdminContentPath, true)
		if (err == nil) != (role == "support") {
			t.Fatal(role, err)
		}
	}
	// A metadata/content-only grant cannot resolve unknown external effects.
	identities.admins[8] = &admindomain.Admin{ID: 8}
	if err = auth.GrantRolePolicy("content_only", c.AdminContentPath, "GET"); err != nil {
		t.Fatal(err)
	}
	if err = auth.SetAdminRoles(8, []string{"content_only"}); err != nil {
		t.Fatal(err)
	}
	if err = container.AuthorizeSupportResolution(context.Background(), 8, 1); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	// A known Telegram user ID without an admins row must not become a backend actor.
	if err = auth.SetAdminRoles(777, []string{"support"}); err != nil {
		t.Fatal(err)
	}
	if err = container.AuthorizeSupportTopicDeletion(context.Background(), 777, 1); !errors.Is(err, c.ErrDenied) {
		t.Fatal("Telegram-only actor confirmed deletion", err)
	}
	if err = container.authorizeSupportAdmin(context.Background(), 777, c.AdminContentPath, true); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	identities.admins[9] = &admindomain.Admin{ID: 9, IsSuper: true}
	if err = container.authorizeSupportAdmin(context.Background(), 9, c.AdminContentPath, true); err != nil {
		t.Fatal(err)
	}
	if err = container.auditSupportContent(context.Background(), 1, 2, 3, "viewed"); !errors.Is(err, c.ErrAdminUnavailable) {
		t.Fatal("missing audit did not fail closed", err)
	}
}

type googleRuntimeSettingStore struct {
	value jsonmap.JSON
	found bool
	err   error
}

func (s *googleRuntimeSettingStore) GetByKey(key string) (jsonmap.JSON, bool, error) {
	if key != constants.SettingKeyGoogleAuthConfig {
		return nil, false, nil
	}
	return s.value, s.found, s.err
}

func (*googleRuntimeSettingStore) Upsert(string, jsonmap.JSON) (jsonmap.JSON, error) {
	return nil, errors.New("unexpected upsert")
}

func TestLoadRuntimeSettingsGoogleAuthThreeStateFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		store       *googleRuntimeSettingStore
		wantEnabled bool
		wantClient  string
	}{
		{
			name: "database disabled overrides enabled yaml",
			store: &googleRuntimeSettingStore{
				found: true,
				value: jsonmap.JSON{"enabled": false, "client_id": "database-client"},
			},
			wantEnabled: false,
			wantClient:  "database-client",
		},
		{
			name:        "missing database setting keeps yaml",
			store:       &googleRuntimeSettingStore{},
			wantEnabled: true,
			wantClient:  "yaml-client",
		},
		{
			name: "database read failure disables yaml fallback",
			store: &googleRuntimeSettingStore{
				err: errors.New("database unavailable"),
			},
			wantEnabled: false,
			wantClient:  "yaml-client",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			container := &Container{
				Config: &config.Config{
					GoogleAuth: config.GoogleAuthConfig{
						Enabled:  true,
						ClientID: "yaml-client",
					},
				},
				SettingService: settingsapp.NewService(test.store),
			}
			container.loadRuntimeSettings()
			if container.Config.GoogleAuth.Enabled != test.wantEnabled {
				t.Fatalf("enabled = %v, want %v", container.Config.GoogleAuth.Enabled, test.wantEnabled)
			}
			if container.Config.GoogleAuth.ClientID != test.wantClient {
				t.Fatalf("client ID = %q, want %q", container.Config.GoogleAuth.ClientID, test.wantClient)
			}
		})
	}
}

func TestNewPaymentProviderRegistryRegistersEveryAdapter(t *testing.T) {
	registry := newPaymentProviderRegistry()
	tests := []struct {
		name         string
		providerType string
		channelType  string
		wantType     string
	}{
		{name: "stripe", providerType: constants.PaymentProviderOfficial, channelType: constants.PaymentChannelTypeStripe, wantType: "official:stripe"},
		{name: "paypal", providerType: constants.PaymentProviderOfficial, channelType: constants.PaymentChannelTypePaypal, wantType: "official:paypal"},
		{name: "wechat", providerType: constants.PaymentProviderOfficial, channelType: constants.PaymentChannelTypeWechat, wantType: "official:wechat"},
		{name: "alipay", providerType: constants.PaymentProviderOfficial, channelType: constants.PaymentChannelTypeAlipay, wantType: "official:alipay"},
		{name: "epay", providerType: constants.PaymentProviderEpay, channelType: "fallback-channel", wantType: "epay:"},
		{name: "epusdt", providerType: constants.PaymentProviderEpusdt, channelType: "fallback-channel", wantType: "epusdt:"},
		{name: "bepusdt", providerType: constants.PaymentProviderBepusdt, channelType: "fallback-channel", wantType: "bepusdt:"},
		{name: "dujiaopay", providerType: constants.PaymentProviderDujiaoPay, channelType: "fallback-channel", wantType: "dujiaopay:"},
		{name: "tokenpay", providerType: constants.PaymentProviderTokenpay, channelType: "fallback-channel", wantType: "tokenpay:"},
		{name: "okpay", providerType: constants.PaymentProviderOkpay, channelType: "fallback-channel", wantType: "okpay:"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, ok := registry.Lookup(test.providerType, test.channelType)
			if !ok {
				t.Fatalf("provider %s channel %s is not registered", test.providerType, test.channelType)
			}
			if got := adapter.Type(); got != test.wantType {
				t.Fatalf("adapter type want %q got %q", test.wantType, got)
			}
		})
	}

	if _, ok := registry.Lookup(constants.PaymentProviderOfficial, "unknown"); ok {
		t.Fatal("official provider must not fall back across channel types")
	}
}
