package integrationtest

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"reflect"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	telegramauthapp "github.com/XManCodeing/XMall/internal/modules/identity/telegramauth/application"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userauthapp "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	userauthstore "github.com/XManCodeing/XMall/internal/modules/identity/userauth/infrastructure/gormstore"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func setupWebsiteTelegramTransfer(t *testing.T, verifiers ...*telegramauthapp.Service) (*userauthapp.Service, *gorm.DB, *userdomain.User, *userdomain.User) {
	t.Helper()
	verifier := telegramauthapp.NewService(config.TelegramAuthConfig{Enabled: true, BotToken: "transfer-test-token", LoginExpireSeconds: 300, ReplayTTLSeconds: 300}, telegramauthapp.WithReplaySetNX(func(context.Context, string, interface{}, time.Duration) (bool, error) { return true, nil }))
	if len(verifiers) != 0 {
		verifier = verifiers[0]
	}
	svc, _, db := setupTelegramOAuthTestService(t, verifier)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&runtimedomain.Instance{}, &runtimedomain.Contact{}, &delivery.OrderSource{}); err != nil {
		t.Fatal(err)
	}
	old := &userdomain.User{Email: "telegram_88001@login.local", PasswordHash: "external-only", PasswordSetupRequired: true, Status: constants.UserStatusActive}
	now := time.Now()
	hash, err := bcrypt.GenerateFromPassword([]byte("website-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	current := &userdomain.User{Email: "website@example.com", PasswordHash: string(hash), Status: constants.UserStatusActive, EmailVerifiedAt: &now}
	for _, u := range []*userdomain.User{old, current} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&externalidentitydomain.Identity{UserID: old.ID, Provider: "telegram", ProviderUserID: "88001"}).Error; err != nil {
		t.Fatal(err)
	}
	return svc, db, old, current
}

func websiteTelegramBind(t *testing.T, svc *userauthapp.Service, userID uint, widget bool) (*externalidentitydomain.Identity, error) {
	t.Helper()
	now := time.Now().Unix()
	if widget {
		payload := telegramauthapp.LoginPayload{ID: 88001, FirstName: "Buyer", AuthDate: now}
		secret := sha256.Sum256([]byte("transfer-test-token"))
		mac := hmac.New(sha256.New, secret[:])
		fmt.Fprintf(mac, "auth_date=%d\nfirst_name=Buyer\nid=88001", now)
		payload.Hash = hex.EncodeToString(mac.Sum(nil))
		return svc.BindTelegram(userauthapp.BindTelegramInput{UserID: userID, Payload: payload})
	}
	data := buildUserAuthTestTelegramMiniAppInitData(t, "transfer-test-token", now, `{"id":88001,"first_name":"Buyer"}`)
	return svc.BindTelegramMiniApp(userauthapp.BindTelegramMiniAppInput{UserID: userID, InitData: data})
}

func TestWebsiteTelegramTransferTrustedScopesAndReadOnly(t *testing.T) {
	for _, widget := range []bool{false, true} {
		t.Run(fmt.Sprintf("widget=%v", widget), func(t *testing.T) {
			svc, db, old, current := setupWebsiteTelegramTransfer(t)
			for id := uint(7); id <= 13; id++ {
				if err := db.Create(&runtimedomain.Instance{ID: id, TelegramID: int64(id), Mode: "internal"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			contacts := []runtimedomain.Contact{
				{BotID: 7, UserID: old.ID, TelegramUserID: 88001, ChatID: 88001},
				{BotID: 9, UserID: current.ID, TelegramUserID: 88001, ChatID: 88001},
				{BotID: 10, UserID: old.ID, TelegramUserID: 999, ChatID: 999},
				{BotID: 11, UserID: old.ID, TelegramUserID: 88001, ChatID: -100},
			}
			if err := db.Create(&contacts).Error; err != nil {
				t.Fatal(err)
			}
			sources := []delivery.OrderSource{
				{OrderID: 1, BotID: 8, UserID: old.ID, TelegramID: 88001, PrivateChatID: 88001},
				{OrderID: 2, BotID: 7, UserID: old.ID, TelegramID: 88001, PrivateChatID: 88001},
				{OrderID: 3, BotID: 12, UserID: old.ID, TelegramID: 999, PrivateChatID: 999},
				{OrderID: 4, BotID: 13, UserID: current.ID, TelegramID: 88001, PrivateChatID: 88001},
			}
			if err := db.Create(&sources).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Order("id").Find(&sources).Error; err != nil {
				t.Fatal(err)
			}
			var before externalidentitydomain.Identity
			if err := db.Where("user_id = ?", old.ID).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			stale := userauthapp.TelegramIdentityStamp{BotID: 7, TelegramUserID: "88001", UserID: old.ID, IdentityID: before.ID}
			oldGrant := &userauthdomain.TelegramHistoryGrant{BotID: 7, TelegramUserID: "88001", PreviousUserID: 999, CurrentUserID: old.ID, IdentityID: before.ID, Status: userauthdomain.TelegramHistoryActive}
			if err := db.Create(oldGrant).Error; err != nil {
				t.Fatal(err)
			}
			var usersBefore []userdomain.User
			db.Order("id").Find(&usersBefore)
			after, err := websiteTelegramBind(t, svc, current.ID, widget)
			if err != nil {
				t.Fatal(err)
			}
			if after.ID == before.ID || after.UserID != current.ID {
				t.Fatal("binding lifetime not replaced")
			}
			if err := svc.ValidateTelegramIdentityStamp(context.Background(), stale); !errors.Is(err, userauthapp.ErrUserOAuthNotBound) {
				t.Fatalf("old secret stamp accepted: %v", err)
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				return userauthstore.ValidateTelegramIdentityStampInTransaction(context.Background(), tx, stale)
			}); !errors.Is(err, userauthapp.ErrUserOAuthNotBound) {
				t.Fatalf("old mutation stamp accepted: %v", err)
			}
			// A current identity cannot mutate B's resources by changing only owner.
			forged := stale
			forged.IdentityID = after.ID
			if err := svc.ValidateTelegramIdentityStamp(context.Background(), forged); err == nil {
				t.Fatal("history grant authorized old-owner mutation")
			}
			for bot := uint(7); bot <= 14; bot++ {
				ok, err := svc.CanReadTelegramHistory(context.Background(), bot, "88001", current.ID, old.ID)
				if err != nil || ok != (bot == 7 || bot == 8) {
					t.Fatalf("bot %d read=%v err=%v", bot, ok, err)
				}
			}
			if err := db.First(oldGrant, oldGrant.ID).Error; err != nil {
				t.Fatal(err)
			}
			if oldGrant.Status != userauthdomain.TelegramHistoryRevoked {
				t.Fatal("old grants survived")
			}
			var usersAfter []userdomain.User
			db.Order("id").Find(&usersAfter)
			if !reflect.DeepEqual(usersBefore, usersAfter) {
				t.Fatal("transfer changed accounts/financial totals")
			}
			var sourcesAfter []delivery.OrderSource
			db.Order("id").Find(&sourcesAfter)
			if !reflect.DeepEqual(sources, sourcesAfter) {
				t.Fatal("transfer merged order provenance")
			}
			if err := svc.UnbindTelegram(current.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := websiteTelegramBind(t, svc, current.ID, widget); err != nil {
				t.Fatal(err)
			}
			if ok, err := svc.CanReadTelegramHistory(context.Background(), 7, "88001", current.ID, old.ID); err != nil || ok {
				t.Fatalf("history revived: %v %v", ok, err)
			}
		})
	}
}

func TestWebsiteTelegramTransferRejectsUnsafeOwners(t *testing.T) {
	for _, scenario := range []string{"real_account", "password_set", "wrong_placeholder", "target_disabled", "target_other_tg", "source_disabled", "missing_provenance_schema"} {
		t.Run(scenario, func(t *testing.T) {
			svc, db, old, current := setupWebsiteTelegramTransfer(t)
			var err error
			want := userauthapp.ErrUserOAuthIdentityExists
			switch scenario {
			case "real_account":
				err = db.Model(old).Update("email", "real@example.com").Error
			case "password_set":
				err = db.Model(old).Update("password_setup_required", false).Error
			case "wrong_placeholder":
				err = db.Model(old).Update("email", "telegram_99@login.local").Error
			case "target_disabled":
				err = db.Model(current).Update("status", "disabled").Error
				want = userauthapp.ErrUserDisabled
			case "source_disabled":
				err = db.Model(old).Update("status", "disabled").Error
				want = userauthapp.ErrUserDisabled
			case "target_other_tg":
				err = db.Create(&externalidentitydomain.Identity{UserID: current.ID, Provider: "telegram", ProviderUserID: "99"}).Error
				want = userauthapp.ErrUserOAuthAlreadyBound
			case "missing_provenance_schema":
				err = db.Migrator().DropTable(&delivery.OrderSource{})
				want = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := websiteTelegramBind(t, svc, current.ID, false); err == nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			var identity externalidentitydomain.Identity
			if err := db.Where("provider_user_id = ?", "88001").First(&identity).Error; err != nil {
				t.Fatal(err)
			}
			if identity.UserID != old.ID {
				t.Fatal("rejected transfer changed owner")
			}
		})
	}
}

type websiteOIDCTransport func(*http.Request) (*http.Response, error)

func (f websiteOIDCTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebsiteTelegramOIDCTransferAndSessionBinding(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": "https://oauth.telegram.org", "aud": "12345", "sub": "legacy-subject", "id": 88001, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	// All OIDC requests are intercepted in-process; no network/config secrets.
	original := http.DefaultTransport
	http.DefaultTransport = websiteOIDCTransport(func(r *http.Request) (*http.Response, error) {
		var payload interface{}
		switch r.URL.String() {
		case "https://oauth.telegram.org/token":
			payload = map[string]string{"id_token": signed}
		case "https://oauth.telegram.org/.well-known/jwks.json":
			payload = map[string]interface{}{"keys": []map[string]string{{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		default:
			return nil, fmt.Errorf("unexpected OIDC request: %s", r.URL)
		}
		body, err := json.Marshal(payload)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, err
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	states := map[string]string{}
	verifier := telegramauthapp.NewService(config.TelegramAuthConfig{Enabled: true, BotToken: "12345:test-token", ClientSecret: "oidc-test-secret", OIDCRedirectURI: "https://shop.example/callback"}, telegramauthapp.WithOIDCStateStore(
		func(_ context.Context, key, value string, _ int) (bool, error) { states[key] = value; return true, nil },
		func(_ context.Context, key string) (string, bool, error) {
			value, ok := states[key]
			delete(states, key)
			return value, ok, nil
		},
	), telegramauthapp.WithReplaySetNX(func(context.Context, string, interface{}, time.Duration) (bool, error) { return true, nil }))
	svc, db, old, current := setupWebsiteTelegramTransfer(t, verifier)
	// Legacy OIDC aliases must be canonicalized under the transfer transaction.
	if err := db.Model(&externalidentitydomain.Identity{}).Where("user_id = ?", old.ID).Update("provider_user_id", "legacy-subject").Error; err != nil {
		t.Fatal(err)
	}
	start := func() string {
		authURL, err := svc.StartTelegramOIDC(userauthapp.StartTelegramOIDCInput{Intent: telegramauthapp.IntentBind, UserID: current.ID})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Query().Get("state")
	}
	if _, err := svc.BindTelegramOIDC(userauthapp.BindTelegramOIDCInput{UserID: old.ID, State: start(), Code: "verified-code"}); !errors.Is(err, telegramauthapp.ErrTelegramAuthPayloadInvalid) {
		t.Fatalf("wrong session: %v", err)
	}
	identity, err := svc.BindTelegramOIDC(userauthapp.BindTelegramOIDCInput{UserID: current.ID, State: start(), Code: "verified-code"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != current.ID || identity.ProviderUserID != "88001" {
		t.Fatalf("OIDC transfer=%+v", identity)
	}
}

func TestWebsiteTelegramTransferRollsBackGrantFailure(t *testing.T) {
	svc, db, old, current := setupWebsiteTelegramTransfer(t)
	if err := db.Create(&runtimedomain.Instance{ID: 7, TelegramID: 7, Mode: "internal"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&runtimedomain.Contact{BotID: 7, UserID: old.ID, TelegramUserID: 88001, ChatID: 88001}).Error; err != nil {
		t.Fatal(err)
	}
	var before externalidentitydomain.Identity
	if err := db.Where("user_id = ?", old.ID).First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("fail_website_grant", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_history_grants" {
			tx.AddError(errors.New("grant failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := websiteTelegramBind(t, svc, current.ID, false); err == nil {
		t.Fatal("expected rollback")
	}
	var after externalidentitydomain.Identity
	if err := db.Where("provider_user_id = ?", "88001").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.UserID != old.ID {
		t.Fatal("failed grant left partial transfer")
	}
}

func TestWebsiteTelegramTransferConcurrentTargets(t *testing.T) {
	svc, db, _, first := setupWebsiteTelegramTransfer(t)
	second := &userdomain.User{Email: "second@example.com", PasswordHash: "password", Status: constants.UserStatusActive}
	if err := db.Create(second).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []uint{first.ID, second.ID} {
		go func(id uint) { <-start; _, err := websiteTelegramBind(t, svc, id, false); results <- err }(id)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				success++
			} else if !errors.Is(err, userauthapp.ErrUserOAuthIdentityExists) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("single-connection transaction deadlock")
		}
	}
	if success != 1 {
		t.Fatalf("successful targets=%d", success)
	}
	var count int64
	if err := db.Model(&userauthdomain.TelegramHistoryGrant{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("no provenance must not grant any bot")
	}
}
