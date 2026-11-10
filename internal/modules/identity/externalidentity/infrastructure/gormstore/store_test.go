package externalidentitystore

import (
	"errors"
	"testing"
	"time"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"

	externalidentitycontract "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestTelegramBindingGuardWithoutPrivateChat 验证网站购买不依赖 Bot 或联系人表。
func TestTelegramBindingGuardWithoutPrivateChat(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := db.AutoMigrate(&userdomain.User{}, &externalidentitydomain.Identity{}); err != nil {
		t.Fatal(err)
	}
	user := userdomain.User{Email: "binding@example.test", Status: "active"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	identity := externalidentitydomain.Identity{UserID: user.ID, Provider: "telegram", ProviderUserID: "9007199254740993"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	binding := externalidentitycontract.TelegramBinding{ID: identity.ID, TelegramID: 9007199254740993}
	cases := []struct {
		name      string
		userID    uint
		binding   externalidentitycontract.TelegramBinding
		wantError bool
	}{
		{"website without bot", user.ID, binding, false},
		{"missing user", 0, binding, true},
		{"other account", user.ID + 1, binding, true},
		{"stale generation", user.ID, externalidentitycontract.TelegramBinding{ID: identity.ID + 1, TelegramID: binding.TelegramID}, true},
		{"other telegram", user.ID, externalidentitycontract.TelegramBinding{ID: identity.ID, TelegramID: 123}, true},
		{"missing generation", user.ID, externalidentitycontract.TelegramBinding{TelegramID: binding.TelegramID}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				return New(tx).LockTelegramBinding(testCase.userID, testCase.binding)
			})
			if testCase.wantError && !errors.Is(err, externalidentitycontract.ErrTelegramIdentityChanged) {
				t.Fatalf("expected identity conflict, got %v", err)
			}
			if !testCase.wantError && err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, updates := range []map[string]any{{"status": "disabled"}, {"status": "active", "deleted_at": time.Now()}} {
		if err := db.Model(&userdomain.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		err := db.Transaction(func(tx *gorm.DB) error { return New(tx).LockTelegramBinding(user.ID, binding) })
		if !errors.Is(err, externalidentitycontract.ErrTelegramIdentityChanged) {
			t.Fatalf("inactive account accepted: %v", err)
		}
	}
}

// TestTelegramBindingGuardRejectsRebinding 验证同一 Telegram 解绑重绑后旧快照失效，事务错误不留下业务写入。
func TestTelegramBindingGuardRejectsRebinding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := db.AutoMigrate(&userdomain.User{}, &externalidentitydomain.Identity{}); err != nil {
		t.Fatal(err)
	}
	user := userdomain.User{Email: "rebind@example.test", Status: "active", DisplayName: "original"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	identity := externalidentitydomain.Identity{UserID: user.ID, Provider: "telegram", ProviderUserID: "12345"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	oldBinding := externalidentitycontract.TelegramBinding{ID: identity.ID, TelegramID: 12345}
	if err := db.Delete(&identity).Error; err != nil {
		t.Fatal(err)
	}
	identity.ID = 0
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	if identity.ID == oldBinding.ID {
		t.Fatal("rebinding reused identity generation")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&userdomain.User{}).Where("id = ?", user.ID).Update("display_name", "must rollback").Error; err != nil {
			return err
		}
		return New(tx).LockTelegramBinding(user.ID, oldBinding)
	})
	if !errors.Is(err, externalidentitycontract.ErrTelegramIdentityChanged) {
		t.Fatalf("stale binding accepted: %v", err)
	}
	var persisted userdomain.User
	if err := db.First(&persisted, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.DisplayName != "original" {
		t.Fatal("identity conflict did not roll back transaction")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return New(tx).LockTelegramBinding(user.ID, externalidentitycontract.TelegramBinding{ID: identity.ID, TelegramID: 12345})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestListTelegramUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&userdomain.User{}, &externalidentitydomain.Identity{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	user1 := &userdomain.User{
		Email:        "alice@example.com",
		PasswordHash: "hash",
		DisplayName:  "Alice",
		Status:       "active",
	}
	user2 := &userdomain.User{
		Email:        "bob@example.com",
		PasswordHash: "hash",
		DisplayName:  "Bob",
		Status:       "active",
	}
	if err := db.Create(user1).Error; err != nil {
		t.Fatalf("create user1 failed: %v", err)
	}
	if err := db.Create(user2).Error; err != nil {
		t.Fatalf("create user2 failed: %v", err)
	}

	boundAt1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	boundAt2 := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	identity1 := &externalidentitydomain.Identity{
		UserID:         user1.ID,
		Provider:       "telegram",
		ProviderUserID: "10001",
		Username:       "alice_tg",
		CreatedAt:      boundAt1,
		UpdatedAt:      boundAt1,
	}
	identity2 := &externalidentitydomain.Identity{
		UserID:         user2.ID,
		Provider:       "telegram",
		ProviderUserID: "20002",
		Username:       "bob_tg",
		CreatedAt:      boundAt2,
		UpdatedAt:      boundAt2,
	}
	if err := db.Create(identity1).Error; err != nil {
		t.Fatalf("create identity1 failed: %v", err)
	}
	if err := db.Create(identity2).Error; err != nil {
		t.Fatalf("create identity2 failed: %v", err)
	}

	repo := New(db)

	items, total, err := repo.ListTelegramUsers(externalidentitycontract.TelegramUserFilter{
		Keyword:  "alice",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list telegram users failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total 1, got %d", total)
	}
	if len(items) != 1 || items[0].TelegramUserID != "10001" {
		t.Fatalf("unexpected list result: %+v", items)
	}

	items, total, err = repo.ListTelegramUsers(externalidentitycontract.TelegramUserFilter{
		TelegramUserID: "200",
		Page:           1,
		PageSize:       10,
	})
	if err != nil {
		t.Fatalf("list by telegram id failed: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].UserID != user2.ID {
		t.Fatalf("unexpected list by telegram id: total=%d items=%+v", total, items)
	}

	items, total, err = repo.ListTelegramUsers(externalidentitycontract.TelegramUserFilter{
		CreatedFrom: ptrTime(boundAt2.Add(-time.Hour)),
		CreatedTo:   ptrTime(boundAt2.Add(time.Hour)),
		Page:        1,
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("list by created range failed: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].TelegramUsername != "bob_tg" {
		t.Fatalf("unexpected list by created range: total=%d items=%+v", total, items)
	}

	got, err := repo.GetByProviderUserID(" Telegram ", "10001")
	if err != nil {
		t.Fatalf("get normalized provider identity failed: %v", err)
	}
	if got == nil || got.ID != identity1.ID {
		t.Fatalf("unexpected normalized provider identity: %#v", got)
	}
	got.Username = "alice_updated"
	if err := repo.Update(got); err != nil {
		t.Fatalf("update identity failed: %v", err)
	}
	byUser, err := repo.GetByUserProvider(user1.ID, "telegram")
	if err != nil {
		t.Fatalf("get user provider identity failed: %v", err)
	}
	if byUser == nil || byUser.Username != "alice_updated" {
		t.Fatalf("identity update was not persisted: %#v", byUser)
	}
	if err := repo.DeleteByID(identity1.ID); err != nil {
		t.Fatalf("delete identity failed: %v", err)
	}
	deleted, err := repo.GetByProviderUserID("telegram", "10001")
	if err != nil {
		t.Fatalf("get deleted identity failed: %v", err)
	}
	if deleted != nil {
		t.Fatalf("deleted identity must be absent: %#v", deleted)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
