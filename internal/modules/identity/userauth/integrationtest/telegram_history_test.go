package integrationtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	emailverificationdomain "github.com/XManCodeing/XMall/internal/modules/identity/emailverification/domain"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userauthapp "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	userauthstore "github.com/XManCodeing/XMall/internal/modules/identity/userauth/infrastructure/gormstore"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func setupTelegramHistory(t *testing.T) (*userauthapp.Service, *gorm.DB, *userdomain.User, *userdomain.User, userauthapp.BindTelegramChannelByEmailCodeInput) {
	t.Helper()
	svc, db := setupUserAuthServiceChannelIdentityTest(t)
	if err := db.AutoMigrate(&userauthdomain.TelegramHistoryGrant{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	old := &userdomain.User{Email: "telegram_88001@login.local", PasswordHash: "external-only", PasswordSetupRequired: true, Status: constants.UserStatusActive}
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	current := &userdomain.User{Email: "history@example.com", PasswordHash: string(hash), Status: constants.UserStatusActive}
	for _, u := range []*userdomain.User{old, current} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&externalidentitydomain.Identity{UserID: old.ID, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "88001"}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&emailverificationdomain.Code{Email: current.Email, Purpose: constants.VerifyPurposeTelegramBind, Code: "654321", ExpiresAt: now.Add(time.Hour), SentAt: now, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	return svc, db, old, current, userauthapp.BindTelegramChannelByEmailCodeInput{Identity: userauthapp.TelegramChannelIdentityInput{ChannelUserID: "88001"}, Email: current.Email, Code: "654321"}
}

func TestTelegramHistoryRebindRevokeAndNoRevival(t *testing.T) {
	svc, db, old, current, input := setupTelegramHistory(t)
	user, identity, previous, err := svc.BindTelegramBotByEmailCode(7, input)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != current.ID || identity.UserID != current.ID || previous != old.ID {
		t.Fatal("incorrect rebind")
	}
	assertHistory := func(bot uint, tg string, owner uint, want bool) {
		t.Helper()
		ok, err := svc.CanReadTelegramHistory(context.Background(), bot, tg, current.ID, owner)
		if err != nil || ok != want {
			t.Fatalf("history=%v err=%v want %v", ok, err, want)
		}
	}
	assertHistory(7, "88001", old.ID, true)
	assertHistory(8, "88001", old.ID, false)
	assertHistory(7, "88001", current.ID, false)
	if err := db.Model(old).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	assertHistory(7, "88001", old.ID, false)
	if err := db.Model(old).Update("status", constants.UserStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	assertHistory(7, "88001", old.ID, true)
	if ok, _ := svc.CanReadTelegramHistory(context.Background(), 7, "88002", current.ID, old.ID); ok {
		t.Fatal("wrong telegram allowed")
	}
	if err := db.Model(current).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.CanReadTelegramHistory(context.Background(), 7, "88001", current.ID, old.ID); ok || !errors.Is(err, userauthapp.ErrUserDisabled) {
		t.Fatalf("disabled access: %v %v", ok, err)
	}
	if err := db.Model(current).Update("status", constants.UserStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.UnbindTelegram(current.ID); err != nil {
		t.Fatal(err)
	}
	var grant userauthdomain.TelegramHistoryGrant
	if err := db.First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.Status != userauthdomain.TelegramHistoryRevoked || grant.RevokedAt == nil {
		t.Fatal("grant not revoked")
	}
	if ok, _ := svc.CanReadTelegramHistory(context.Background(), 7, "88001", current.ID, old.ID); ok {
		t.Fatal("unbound access")
	}
	// Simulate a subsequent verified website binding (new identity lifetime).
	if err := db.Create(&externalidentitydomain.Identity{UserID: current.ID, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "88001"}).Error; err != nil {
		t.Fatal(err)
	}
	assertHistory(7, "88001", old.ID, false)
	var oldAfter userdomain.User
	if err := db.First(&oldAfter, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if oldAfter.Email != old.Email {
		t.Fatal("old account mutated")
	}
}

func TestTelegramHistoryTransferRejectsMismatchedPlaceholder(t *testing.T) {
	svc, db, old, _, input := setupTelegramHistory(t)
	if err := db.Model(old).Update("email", "telegram_99999@login.local").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.BindTelegramBotByEmailCode(7, input); !errors.Is(err, userauthapp.ErrUserOAuthIdentityExists) {
		t.Fatalf("error=%v", err)
	}
	var grants int64
	db.Model(&userauthdomain.TelegramHistoryGrant{}).Count(&grants)
	if grants != 0 {
		t.Fatal("unexpected grant")
	}
}

func TestTelegramHistoryTransferRollsBackWithGrantFailure(t *testing.T) {
	svc, db, old, _, input := setupTelegramHistory(t)
	if err := db.Callback().Create().Before("gorm:create").Register("fail_history_grant", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_history_grants" {
			tx.AddError(errors.New("injected grant failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.BindTelegramBotByEmailCode(7, input); err == nil {
		t.Fatal("expected failure")
	}
	var identity externalidentitydomain.Identity
	if err := db.Where("provider_user_id = ?", "88001").First(&identity).Error; err != nil {
		t.Fatal(err)
	}
	if identity.UserID != old.ID {
		t.Fatal("identity moved without grant")
	}
}

func TestTelegramHistoryConcurrentBindsCreateOneGrant(t *testing.T) {
	svc, db, old, current, input := setupTelegramHistory(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _, err := svc.BindTelegramBotByEmailCode(7, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("no bind succeeded")
	}
	var grants []userauthdomain.TelegramHistoryGrant
	if err := db.Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].CurrentUserID != current.ID || grants[0].PreviousUserID != old.ID {
		t.Fatalf("grants=%+v", grants)
	}
}

func TestTelegramHistoryConcurrentResolveAndUnbindCannotRestore(t *testing.T) {
	svc, db, old, current, input := setupTelegramHistory(t)
	if _, _, _, err := svc.BindTelegramBotByEmailCode(7, input); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, err := svc.ResolveTelegramChannelIdentity(input.Identity)
		errs <- err
	}()
	go func() { defer wg.Done(); <-start; errs <- svc.UnbindTelegram(current.ID) }()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&externalidentitydomain.Identity{}).Where("provider_user_id = ?", "88001").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("resolver restored deleted identity")
	}
	if ok, _ := svc.CanReadTelegramHistory(context.Background(), 7, "88001", current.ID, old.ID); ok {
		t.Fatal("revoked history allowed")
	}
}

func TestTelegramIdentityStampChangesOnTransferAndRebind(t *testing.T) {
	svc, db, old, current, input := setupTelegramHistory(t)
	_, before, err := svc.ResolveTelegramChannelIdentity(input.Identity)
	if err != nil {
		t.Fatal(err)
	}
	oldStamp := userauthapp.TelegramIdentityStamp{BotID: 7, TelegramUserID: "88001", UserID: old.ID, IdentityID: before.ID}
	if err := svc.ValidateTelegramIdentityStamp(context.Background(), oldStamp); err != nil {
		t.Fatal(err)
	}
	_, after, _, err := svc.BindTelegramBotByEmailCode(7, input)
	if err != nil {
		t.Fatal(err)
	}
	if before.ID == after.ID {
		t.Fatal("transfer reused binding lifetime")
	}
	if err := svc.ValidateTelegramIdentityStamp(context.Background(), oldStamp); err == nil {
		t.Fatal("old stamp valid")
	}
	stamp := userauthapp.TelegramIdentityStamp{BotID: 7, TelegramUserID: "88001", UserID: current.ID, IdentityID: after.ID}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return userauthstore.ValidateTelegramIdentityStampInTransaction(context.Background(), tx, stamp)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(current).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTelegramIdentityStamp(context.Background(), stamp); !errors.Is(err, userauthapp.ErrUserDisabled) {
		t.Fatalf("disabled stamp err=%v", err)
	}
	// Shared administrator/user revocation must remain available while disabled.
	if err := svc.UnbindTelegram(current.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(current).Update("status", constants.UserStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	fresh := &externalidentitydomain.Identity{UserID: current.ID, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "88001"}
	if err := db.Create(fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTelegramIdentityStamp(context.Background(), stamp); err == nil {
		t.Fatal("old payload revived")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return userauthstore.ValidateTelegramIdentityStampInTransaction(context.Background(), tx, stamp)
	}); err == nil {
		t.Fatal("transaction accepted obsolete stamp")
	}
}
