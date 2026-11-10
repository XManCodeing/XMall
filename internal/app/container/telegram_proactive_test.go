package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
	"time"
)

// TestProactiveContainerWithoutSession exercises real assembly, queue, authorization and receipts.
// It deliberately creates no shopping session and never equates enqueue with a delivered invitation.
func TestProactiveContainerWithoutSession(t *testing.T) {
	fixture := newMembershipAccessFixture(t, true)
	models := append(s.ProactiveModels(), &d.LedgerOutbox{})
	if err := fixture.db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&rd.Contact{BotID: 1, TelegramUserID: membershipAssemblyTelegramID, ChatID: membershipAssemblyTelegramID, UserID: 7, Reachable: true, Locale: "en-US"}).Error; err != nil {
		t.Fatal(err)
	}
	outbox := d.LedgerOutbox{OrderItemID: 1, TargetID: 2, Operation: "grant", BeneficiaryTelegramID: membershipAssemblyTelegramID, State: "pending", AvailableAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	fixture.bridge.Proactive.Now = func() time.Time { return fixture.now }
	for tick := 0; tick < 3; tick++ {
		if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
			t.Fatal(err)
		}
		fixture.now = fixture.now.Add(3 * time.Second)
	}
	// No network lane has run; exactly one independent slow feedback appears after 15 seconds.
	fixture.now = fixture.now.Add(16 * time.Second)
	if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	var task d.ProactiveDelivery
	if err := fixture.db.First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.State != "preparing" || task.InviteID == 0 {
		t.Fatalf("preparation: %+v", task)
	}
	var delivery rd.Delivery
	if err := fixture.db.Last(&delivery, "purpose = ?", "membership_proactive").Error; err != nil {
		t.Fatal(err)
	}
	raw, err := fixture.bridge.Proactive.Cipher.Open(delivery.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	var message r.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	if message.ProactiveDelivery == nil || message.ProactiveDelivery.Stage != "slow" || message.PanelEpoch != "" || message.Operation != "send" || message.IdentityStamp != "1:7" {
		t.Fatalf("wrong envelope: %+v", message)
	}
	if err := fixture.bridge.AuthorizeProactiveDelivery(fixture.ctx, 1, message); err != nil {
		t.Fatal(err)
	}
	message.Purpose = "panel"
	if err := fixture.bridge.AuthorizeProactiveDelivery(fixture.ctx, 1, message); err == nil {
		t.Fatal("accepted forged panel")
	}
	var sessions int64
	if err := fixture.db.Model(&rd.Session{}).Count(&sessions).Error; err != nil || sessions != 0 {
		t.Fatalf("session touched: %d %v", sessions, err)
	}
	fixture.now = fixture.now.Add(3 * time.Second)
	if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "membership_proactive").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("duplicate preparation/slow feedback: %d %v", count, err)
	}
	assertProactiveReadyUnknown(t, fixture, task, delivery)
}

// assertProactiveReadyUnknown verifies long-term button authorization and retained unknown responsibility.
func assertProactiveReadyUnknown(t *testing.T, fixture *membershipAccessFixture, task d.ProactiveDelivery, delivery rd.Delivery) {
	t.Helper()
	var count int64
	var message r.Message
	// Inject a verified platform creation receipt, then exercise real invite preparation/guard.
	cipher, err := fixture.service.Cipher.Seal(membershipAssemblyLink)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(membershipAssemblyLink))
	if err := fixture.db.Model(&d.AccessInvite{}).Where("id = ?", task.InviteID).Updates(map[string]any{"state": "active", "term": d.InviteTermLongTerm, "expiry_trusted": true, "link_cipher": cipher, "link_hash": hex.EncodeToString(digest[:]), "expires_at": time.Time{}}).Error; err != nil {
		t.Fatal(err)
	}
	for tick := 0; tick < 2; tick++ {
		fixture.now = fixture.now.Add(3 * time.Second)
		if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	var ready rd.Delivery
	if err := fixture.db.Where("purpose = ? AND id > ?", "membership_proactive", delivery.ID).First(&ready).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := fixture.bridge.Proactive.Cipher.Open(ready.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	if message.InviteDelivery == nil || len(message.Buttons) != 1 || message.Buttons[0][0].URL != membershipAssemblyLink {
		t.Fatal("missing independent invitation button")
	}
	if err := fixture.bridge.AuthorizeProactiveDelivery(fixture.ctx, 1, message); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&ready).Updates(map[string]any{"status": "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(3 * time.Second)
	if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&task, task.ID).Error; err != nil || task.State != "unknown" {
		t.Fatalf("unknown responsibility lost: %s %v", task.State, err)
	}
	fixture.now = fixture.now.Add(3 * time.Second)
	if err := fixture.bridge.ReconcileProactive(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "membership_proactive").Count(&count).Error; err != nil || count != 4 {
		t.Fatalf("unknown replayed: %d %v", count, err)
	}
}
