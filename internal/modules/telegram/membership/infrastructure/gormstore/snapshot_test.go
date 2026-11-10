package gormstore_test

import (
	"context"
	"encoding/json"
	"errors"
	ei "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/contract"
	os "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"reflect"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	eid "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	ud "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"gorm.io/gorm"
)

// TestQuotationEmptyProjectionAllowsIdentityMigration exercises the real overlap
// precheck before migration; a quotation must not become a purchased asset conflict.
func TestQuotationEmptyProjectionAllowsIdentityMigration(t *testing.T) {
	store, db, input := ledgerSetup(t)
	ctx := context.Background()
	if err := store.GrantPaid(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&d.AccessMember{}, &d.AccessInvite{}, &d.AccessOperation{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("user_oauth_identities").Where("user_id = ? AND provider = ?", 7, "telegram").Update("provider_user_id", "9007199254740995").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermanentOverlap(ctx, 7, 9007199254740995, []d.SnapshotTarget{{TargetID: 1}}); err != nil {
		t.Fatal(err)
	}
	writer, err := im.New(db)
	if err != nil {
		t.Fatal(err)
	}
	request := c.IdentityMigrationInput{AccountID: 7, OldTelegramID: "9007199254740993", NewTelegramID: "9007199254740995", TargetIDs: []string{"1"}}
	preview, err := writer.PreviewIdentityMigration(ctx, request, input.PaidAt)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedVersions = map[string]string{"1": preview.Targets[0].EntitlementVersion}
	commit := c.IdentityMigrationCommit{IdentityMigrationInput: request, ActorAdminID: 3, HumanVerified: true, ExpectedBindingVersion: preview.BindingVersion, ExpectedRoutes: map[string]string{"1": preview.Targets[0].RouteVersion}, ExpectedGroups: map[string]string{"1": preview.Targets[0].GroupVersion}}
	if err := writer.CommitIdentityMigration(ctx, commit, input.PaidAt); err != nil {
		t.Fatal(err)
	}
	if ledgerCount(t, db, &d.LedgerSource{}) != 1 {
		t.Fatal("quotation created payment facts")
	}
}

// TestProactiveScanFairAndIsolated verifies live consumption, minute-paced history and per-source commits.
// A malformed oldest grant must neither roll back new orders nor stall healthy history.
func TestProactiveScanFairAndIsolated(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(s.ProactiveModels()...); err != nil {
		t.Fatal(err)
	}
	var template d.LedgerSource
	if err := harness.db.First(&template).Error; err != nil {
		t.Fatal(err)
	}
	// Explicitly predate startup: these sources must use the low-frequency lane.
	template.CreatedAt = harness.now.Add(-time.Hour)
	if err := harness.db.Model(&d.LedgerSource{}).Where("id = ?", template.ID).Update("created_at", template.CreatedAt).Error; err != nil {
		t.Fatal(err)
	}
	for index := uint(1); index <= 20; index++ {
		source := template
		source.ID, source.OrderItemID = index, index
		if index > 1 {
			if err := harness.db.Create(&source).Error; err != nil {
				t.Fatal(err)
			}
		}
		beneficiary := source.BeneficiaryTelegramID
		if index == 1 {
			beneficiary++
		}
		outbox := d.LedgerOutbox{OrderItemID: index, TargetID: source.TargetID, Operation: "grant", BeneficiaryTelegramID: beneficiary, State: "pending", AvailableAt: harness.now, CreatedAt: harness.now, UpdatedAt: harness.now}
		if err := harness.db.Create(&outbox).Error; err != nil {
			t.Fatal(err)
		}
	}
	count, err := harness.store.ScanProactive(ctx, 9, 4, harness.now)
	if !errors.Is(err, c.ErrConflict) || count != 3 {
		t.Fatalf("failed source rolled back batch: count=%d error=%v", count, err)
	}
	var rows []d.ProactiveDelivery
	if err := harness.db.Order("source_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].SourceID != 2 || rows[1].SourceID != 3 || rows[2].SourceID != 4 {
		t.Fatalf("healthy history rolled back: %+v", rows)
	}
	// A restarted store consumes a new grant immediately, without rescanning history.
	newSource := template
	newSource.ID, newSource.OrderItemID = 21, 21
	if err := harness.db.Create(&newSource).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&d.LedgerOutbox{OrderItemID: 21, TargetID: template.TargetID, Operation: "grant", BeneficiaryTelegramID: template.BeneficiaryTelegramID, State: "pending", AvailableAt: harness.now}).Error; err != nil {
		t.Fatal(err)
	}
	restarted := s.New(harness.db)
	count, err = restarted.ScanProactive(ctx, 9, 4, harness.now.Add(2*time.Second))
	if err != nil || count != 1 {
		t.Fatalf("new grant blocked by history: %d %v", count, err)
	}
	if err := harness.db.Model(&d.ProactiveDelivery{}).Where("source_id = ?", 21).Update("state", "unknown").Error; err != nil {
		t.Fatal(err)
	}
	// A whole bad historical page advances its checkpoint and does not monopolize future pages.
	if err := harness.db.Model(&d.LedgerOutbox{}).Where("order_item_id BETWEEN ? AND ?", 5, 8).Update("beneficiary_telegram_id", 1).Error; err != nil {
		t.Fatal(err)
	}
	count, err = restarted.ScanProactive(ctx, 9, 4, harness.now.Add(time.Minute))
	if !errors.Is(err, c.ErrConflict) || count != 0 {
		t.Fatalf("bad page: %d %v", count, err)
	}
	count, err = restarted.ScanProactive(ctx, 9, 4, harness.now.Add(2*time.Minute))
	if err != nil || count != 4 {
		t.Fatalf("bad page starved later history: %d %v", count, err)
	}
	assertProactiveHistoryWrap(t, harness)
	var retained d.ProactiveDelivery
	if err := harness.db.Where("source_id = ?", 21).First(&retained).Error; err != nil || retained.State != "unknown" {
		t.Fatalf("scan reset unknown: %+v %v", retained, err)
	}
}

// assertProactiveHistoryWrap checks that failed sources are revisited after a bounded sweep.
// The repaired source must be derived without resetting existing delivery responsibility.
func assertProactiveHistoryWrap(t *testing.T, harness *accessHarness) {
	t.Helper()
	for minute := 3; minute <= 5; minute++ {
		if _, err := harness.store.ScanProactive(context.Background(), 9, 4, harness.now.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := harness.db.Model(&d.LedgerOutbox{}).Where("order_item_id = ?", 1).Update("beneficiary_telegram_id", harness.identity.TelegramID).Error; err != nil {
		t.Fatal(err)
	}
	count, err := harness.store.ScanProactive(context.Background(), 9, 4, harness.now.Add(6*time.Minute))
	if count != 1 || !errors.Is(err, c.ErrConflict) {
		t.Fatalf("history did not revisit repaired source: %d %v", count, err)
	}
}

// TestProactiveEmptyScanSkipsLiveWrite verifies an empty live lane never issues a no-op UPDATE.
// t also checks that history still advances its cadence, wraps, and remains minute-paced.
func TestProactiveEmptyScanSkipsLiveWrite(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(s.ProactiveModels()...); err != nil {
		t.Fatal(err)
	}
	checkpoint := d.ProactiveCursor{BotID: 9, FastAfter: 99, HistoryAfter: 12, HistoryAt: harness.now.Add(-time.Minute)}
	if err := harness.db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	liveWrites, historyWrites := 0, 0
	const callback = "test:reject_empty_live_cursor_write"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != checkpoint.TableName() {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			tx.AddError(errors.New("unexpected proactive cursor update shape"))
			return
		}
		if _, exists := updates["fast_after"]; exists {
			liveWrites++
			tx.AddError(errors.New("empty live lane attempted no-op cursor update"))
		} else {
			historyWrites++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(callback); err != nil {
			t.Error(err)
		}
	})
	for _, elapsed := range []time.Duration{0, 0, time.Second, time.Minute} {
		if count, err := harness.store.ScanProactive(ctx, 9, 10, harness.now.Add(elapsed)); count != 0 || err != nil {
			t.Fatalf("empty scan: count=%d err=%v", count, err)
		}
	}
	var actual d.ProactiveCursor
	if err := harness.db.First(&actual, "bot_id = ?", 9).Error; err != nil {
		t.Fatal(err)
	}
	if liveWrites != 0 || historyWrites != 2 || actual.FastAfter != 99 || actual.HistoryAfter != 0 || !actual.HistoryAt.Equal(harness.now.Add(time.Minute)) {
		t.Fatalf("empty scan changed cursor semantics: live=%d history=%d cursor=%+v", liveWrites, historyWrites, actual)
	}
}

// TestProactiveScanRetainsCursorCAS verifies a real live advance still rejects a stale checkpoint.
// t injects a competing cursor update immediately before CAS and checks it cannot regress.
func TestProactiveScanRetainsCursorCAS(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(s.ProactiveModels()...); err != nil {
		t.Fatal(err)
	}
	checkpoint := d.ProactiveCursor{BotID: 9, HistoryAt: harness.now}
	if err := harness.db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	var source d.LedgerSource
	if err := harness.db.First(&source).Error; err != nil {
		t.Fatal(err)
	}
	outbox := d.LedgerOutbox{OrderItemID: source.OrderItemID, TargetID: source.TargetID, Operation: "grant", BeneficiaryTelegramID: source.BeneficiaryTelegramID, State: "pending", AvailableAt: harness.now}
	if err := harness.db.Create(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	injected := false
	const callback = "test:advance_proactive_cursor_before_cas"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != checkpoint.TableName() || injected {
			return
		}
		injected = true
		// Use the current connection, not the one-slot pool, to avoid SQLite self-deadlock.
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE telegram_membership_proactive_cursors SET fast_after = ? WHERE bot_id = ?", 99, 9).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(callback); err != nil {
			t.Error(err)
		}
	})
	if count, err := harness.store.ScanProactive(ctx, 9, 10, harness.now); count != 1 || !errors.Is(err, c.ErrConflict) || !injected {
		t.Fatalf("stale cursor not rejected: count=%d injected=%t err=%v", count, injected, err)
	}
	var actual d.ProactiveCursor
	if err := harness.db.First(&actual, "bot_id = ?", 9).Error; err != nil || actual.FastAfter != 99 {
		t.Fatalf("competing cursor regressed: %+v err=%v", actual, err)
	}
}

// TestExistingMembershipInviteReadOnly verifies repeated retrieval never creates work.
// Missing invitations remain absent; authenticated active entries can be reread.
func TestExistingMembershipInviteReadOnly(t *testing.T) {
	harness, ctx := accessSetup(t)
	entry, err := harness.service.ExistingMembershipInvite(ctx, harness.identity, 1)
	if err != nil || entry == nil || entry.Link != "" || entry.InviteID != 0 {
		t.Fatalf("missing entry: %+v %v", entry, err)
	}
	var count int64
	if err := harness.db.Model(&d.AccessOperation{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("read created an operation: %d %v", count, err)
	}
	active := accessActive(t, harness, ctx)
	for attempt := 0; attempt < 3; attempt++ {
		entry, err = harness.service.ExistingMembershipInvite(ctx, harness.identity, 1)
		if err != nil || entry.InviteID != active.InviteID || entry.Link != active.Link {
			t.Fatalf("existing invitation unavailable: %+v %v", entry, err)
		}
	}
	if err := harness.db.Model(&d.AccessOperation{}).Count(&count).Error; err != nil || count != 1 || harness.calls != 1 {
		t.Fatalf("retrieval produced side effects: %d %v", count, err)
	}
}

// TestExistingMembershipInviteGuard prevents reads from bypassing current authorization.
// State changes and mismatched ciphertext must never expose a usable invitation.
func TestExistingMembershipInviteGuard(t *testing.T) {
	for _, scenario := range []string{"denied", "other_account", "unknown", "wrong_hash", "route"} {
		t.Run(scenario, func(t *testing.T) {
			harness, ctx := accessSetup(t)
			active := accessActive(t, harness, ctx)
			identity := harness.identity
			switch scenario {
			case "denied":
				harness.deny = true
			case "other_account":
				identity.AccountID++
			case "unknown":
				if err := harness.db.Model(&d.AccessInvite{}).Where("id = ?", active.InviteID).Update("state", c.AccessUnknown).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong_hash":
				if err := harness.db.Model(&d.AccessInvite{}).Where("id = ?", active.InviteID).Update("link_hash", "invalid").Error; err != nil {
					t.Fatal(err)
				}
			case "route":
				harness.route.RouteVersion++
			}
			entry, err := harness.service.ExistingMembershipInvite(ctx, identity, 1)
			if err == nil && entry != nil && entry.Link != "" {
				t.Fatal("unauthorized link disclosed")
			}
		})
	}
}

// TestOrderMembershipTargetsScope verifies parent-child immutable target selection and
// current beneficiary isolation. Reconfiguring unrelated purchases cannot widen an order.
func TestOrderMembershipTargetsScope(t *testing.T) {
	harness, ctx := accessSetup(t)
	if err := harness.db.AutoMigrate(&od.Order{}, &od.OrderItem{}, &d.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	parent := uint(10)
	orders := []od.Order{{ID: 10, UserID: 7, OrderNo: "parent"}, {ID: 11, UserID: 7, OrderNo: "child", ParentID: &parent}, {ID: 12, UserID: 7, OrderNo: "other"}, {ID: 13, UserID: 8, OrderNo: "foreign"}}
	if err := harness.db.Create(&orders).Error; err != nil {
		t.Fatal(err)
	}
	for index, purchase := range orders {
		itemID := uint(index + 1)
		item := od.OrderItem{ID: itemID, OrderID: purchase.ID, ProductID: 2, SKUID: 3, Quantity: 1, TitleJSON: jsonmap.JSON{"zh-CN": "会员测试"}}
		if err := harness.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		snapshot := d.OrderItemSnapshot{OrderItemID: itemID, ProductID: 2, SKUID: 3, AccountID: purchase.UserID, BeneficiaryTelegramID: harness.identity.TelegramID, Quantity: 1, Targets: []d.SnapshotTarget{{TargetID: itemID}}}
		if err := harness.db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
	}
	targets, err := harness.store.OrderMembershipTargets(ctx, 7, harness.identity.TelegramID, 10)
	if err != nil || len(targets) != 2 || targets[0] != 1 || targets[1] != 2 {
		t.Fatalf("parent scope: %v %v", targets, err)
	}
	targets, err = harness.store.OrderMembershipTargets(ctx, 7, harness.identity.TelegramID, 11)
	if err != nil || len(targets) != 1 || targets[0] != 2 {
		t.Fatalf("child scope: %v %v", targets, err)
	}
	if _, err := harness.store.OrderMembershipTargets(ctx, 7, harness.identity.TelegramID, 13); err == nil {
		t.Fatal("foreign order exposed")
	}
	if _, err := harness.store.OrderMembershipTargets(ctx, 7, harness.identity.TelegramID+1, 10); err == nil {
		t.Fatal("foreign beneficiary exposed")
	}
	if err := harness.db.Model(&d.OrderItemSnapshot{}).Where("order_item_id = ?", 1).Update("beneficiary_telegram_id", harness.identity.TelegramID+1).Error; err != nil {
		t.Fatal(err)
	}
	targets, err = harness.store.OrderMembershipTargets(ctx, 7, harness.identity.TelegramID, 10)
	if err != nil || len(targets) != 1 || targets[0] != 2 {
		t.Fatalf("snapshot identity ignored: %v %v", targets, err)
	}
}

// TestProactiveReplacementRequiresCurrentPurchase verifies replacement never notifies historical buyers.
// t runs isolated old-route, missing-snapshot and current-route purchases; failures stop the case.
func TestProactiveReplacementRequiresCurrentPurchase(t *testing.T) {
	for _, purchase := range []string{"historical", "missing", "current"} {
		t.Run(purchase, func(t *testing.T) {
			harness, ctx := accessSetup(t)
			if err := harness.db.AutoMigrate(s.ProactiveModels()...); err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"route_version": 2, "telegram_chat_id": "-100999"}).Error; err != nil {
				t.Fatal(err)
			}
			if purchase != "missing" {
				version, chat := uint64(1), int64(-100111)
				if purchase == "current" {
					version, chat = 2, -100999
				}
				snapshot := d.OrderItemSnapshot{OrderItemID: 1, AccountID: harness.identity.AccountID, BeneficiaryTelegramID: harness.identity.TelegramID, Targets: []d.SnapshotTarget{{TargetID: 1, BotInstanceID: 9, RouteVersion: version, TelegramChatID: chat}}}
				if err := harness.db.Create(&snapshot).Error; err != nil {
					t.Fatal(err)
				}
			}
			outbox := d.LedgerOutbox{OrderItemID: 1, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: harness.identity.TelegramID, State: "pending", AvailableAt: harness.now}
			if err := harness.db.Create(&outbox).Error; err != nil {
				t.Fatal(err)
			}
			if count, err := harness.store.ScanProactive(ctx, 9, 1, harness.now); err != nil || count != 1 {
				t.Fatalf("derive replacement purchase: count=%d error=%v", count, err)
			}
			var delivery d.ProactiveDelivery
			if err := harness.db.First(&delivery).Error; err != nil {
				t.Fatal(err)
			}
			expected := "superseded"
			if purchase == "current" {
				expected = "preparing"
			}
			if delivery.State != expected {
				t.Fatalf("purchase=%s state=%s want=%s", purchase, delivery.State, expected)
			}
		})
	}
}

// TestProactiveDerivedLeaseCAS verifies bounded historical derivation, uniqueness and stale-owner rejection.
// All operations run with the existing single-connection SQLite fixture and no contact/session.
func TestProactiveDerivedLeaseCAS(t *testing.T) {
	h, ctx := accessSetup(t)
	if err := h.db.AutoMigrate(s.ProactiveModels()...); err != nil {
		t.Fatal(err)
	}
	outbox := d.LedgerOutbox{OrderItemID: 1, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: h.identity.TelegramID, ProjectionVersion: 1, State: "pending", AvailableAt: h.now, CreatedAt: h.now, UpdatedAt: h.now}
	if err := h.db.Create(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := h.store.ScanProactive(ctx, 9, 1, h.now); err != nil || count != 1 {
		t.Fatalf("derive: %d %v", count, err)
	}
	if count, err := h.store.ScanProactive(ctx, 9, 1, h.now); err != nil || count != 0 {
		t.Fatalf("replay: %d %v", count, err)
	}
	row, err := h.store.ClaimProactive(ctx, 9, "first", h.now, time.Second)
	if err != nil || row == nil {
		t.Fatalf("claim: %v", err)
	}
	if next, err := h.store.ClaimProactive(ctx, 9, "second", h.now, time.Second); err != nil || next != nil {
		t.Fatalf("double claim: %v", err)
	}
	recovered, err := h.store.ClaimProactive(ctx, 9, "second", h.now.Add(2*time.Second), time.Minute)
	if err != nil || recovered == nil {
		t.Fatalf("recovery: %v", err)
	}
	if err := h.store.SaveProactive(ctx, row, h.now.Add(2*time.Second)); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale owner: %v", err)
	}
	recovered.State = "unknown"
	if err := h.store.SaveProactive(ctx, recovered, h.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if count, err := h.store.ScanProactive(ctx, 9, 10, h.now); err != nil || count != 0 {
		t.Fatalf("unknown replay: %d %v", count, err)
	}
	if err := h.store.ValidateProactive(ctx, recovered, h.now); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&d.LedgerRevocation{OrderItemID: 1, TargetID: 1, BeneficiaryTelegramID: h.identity.TelegramID, RevokedAt: h.now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.store.ValidateProactive(ctx, recovered, h.now); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatalf("revoked source: %v", err)
	}
}

// paymentCheck 通过真实订单仓储加载父子单，在单连接事务内执行历史预检。
// 返回复核结果，不安装虚假的无永久资格实现。
func paymentCheck(t *testing.T, db *gorm.DB, now time.Time) error {
	t.Helper()
	return db.Transaction(func(tx *gorm.DB) error {
		order, err := os.UseTransaction(tx, "test-guest-credential-secret-with-32-bytes").Orders().GetByIDForUpdateWithChildren(9)
		if err != nil {
			return err
		}
		checkout, err := s.CheckoutInTransaction(tx, nil)
		if err != nil {
			return err
		}
		_, err = checkout.(c.PaymentPreflight).CheckPayment(context.Background(), order, now)
		return err
	})
}

// TestPaymentUsesHistoricalSnapshot 覆盖混合父子单、删商品/改套餐、身份代次和群能力。
func TestPaymentUsesHistoricalSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*gorm.DB) error
		expected error
	}{
		{"unchanged_requires_ledger", func(*gorm.DB) error { return nil }, c.ErrOverlapUnavailable},
		{"deleted_product", func(db *gorm.DB) error { return db.Delete(&p.Product{}, 1).Error }, c.ErrNotFound},
		{"changed_config", func(db *gorm.DB) error {
			return db.Exec("UPDATE telegram_membership_product_configs SET version = 99 WHERE product_id = 1").Error
		}, c.ErrOverlapUnavailable},
		{"quantity", func(db *gorm.DB) error {
			return db.Model(&od.OrderItem{}).Where("id = ?", 11).Update("quantity", 2).Error
		}, c.ErrInvalid},
		{"account", func(db *gorm.DB) error { return db.Model(&od.Order{}).Where("id = ?", 10).Update("user_id", 8).Error }, c.ErrUntrustedIdentity},
		{"rebind_generation", func(db *gorm.DB) error { return db.Model(&eid.Identity{}).Where("id = ?", 4).Update("id", 14).Error }, ei.ErrTelegramIdentityChanged},
		{"old_route", func(db *gorm.DB) error {
			return db.Model(&gd.Target{}).Where("id = ?", 1).Update("route_version", 2).Error
		}, c.ErrCapability},
		{"no_capability", func(db *gorm.DB) error {
			return db.Model(&gd.Target{}).Where("id = ?", 1).Update("enabled", false).Error
		}, c.ErrCapability},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, db, now := snapshotSetup(t)
			if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
				t.Fatal(err)
			}
			if _, err := insert(t, db, snapshotInput(), now); err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("UPDATE telegram_membership_product_configs SET enabled = ? WHERE product_id = 1", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&od.OrderItem{}).Where("id = 12").Update("product_id", 99).Error; err != nil {
				t.Fatal(err)
			}
			// 父单普通商品与子单会员项混合，不能仅检查父单 Items。
			if err := db.Create(&od.OrderItem{ID: 13, OrderID: 9, ProductID: 99, Quantity: 2, TitleJSON: jsonmap.JSON{"en": "ordinary"}}).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(db); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := paymentCheck(t, db, now); !errors.Is(err, test.expected) {
					t.Fatalf("expected %v got %v", test.expected, err)
				}
			}
		})
	}
}

// TestPaymentOrdinaryDoesNotReadBinding 删除身份表后普通订单仍通过，证明无绑定查询。
func TestPaymentOrdinaryDoesNotReadBinding(t *testing.T) {
	_, db, now := snapshotSetup(t)
	if err := db.Migrator().DropTable(&eid.Identity{}); err != nil {
		t.Fatal(err)
	}
	if err := paymentCheck(t, db, now); err != nil {
		t.Fatal(err)
	}
}

// TestPaymentStaleObservation 拒绝已超过有效期的原路由能力观测。
func TestPaymentStaleObservation(t *testing.T) {
	store, db, now := snapshotSetup(t)
	if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := insert(t, db, snapshotInput(), now); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE telegram_membership_product_configs SET enabled = ? WHERE product_id = 1", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&od.OrderItem{}).Where("id = 12").Update("product_id", 99).Error; err != nil {
		t.Fatal(err)
	}
	if err := paymentCheck(t, db, now.Add(24*time.Hour)); !errors.Is(err, c.ErrCapability) {
		t.Fatal(err)
	}
}

// snapshotSetup 在已有单连接 fixture 上建立真实账号、绑定、父子订单和订单项。
func snapshotSetup(t *testing.T) (*s.Store, *gorm.DB, time.Time) {
	t.Helper()
	store, db, now := setup(t)
	if err := db.AutoMigrate(&ud.User{}, &eid.Identity{}, &od.Order{}, &od.OrderItem{}, &fulfillmentdomain.Fulfillment{}); err != nil {
		t.Fatal(err)
	}
	rows := []any{
		&ud.User{ID: 7, Email: "snapshot@example.com", Status: constants.UserStatusActive},
		&eid.Identity{ID: 4, UserID: 7, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "9007199254740993"},
		&od.Order{ID: 9, OrderNo: "parent", UserID: 7},
		&od.Order{ID: 10, OrderNo: "child", UserID: 7, ParentID: func() *uint { id := uint(9); return &id }()},
		&od.OrderItem{ID: 11, OrderID: 10, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "member"}},
		&od.OrderItem{ID: 12, OrderID: 10, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "member"}},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&p.ProductSKU{}).Where("product_id = ?", 1).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	return store, db, now
}

// identity 为 t 返回测试模拟的可信绑定，使用大于 JS 安全整数的 Telegram ID。
func identity(t *testing.T) c.TrustedIdentity {
	t.Helper()
	value, err := c.IdentityFromVerifiedBinding(7, 9007199254740993, 4)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// snapshotInput 返回测试订单项的可信引用，不包含可由客户端伪造的身份字段。
func snapshotInput() c.SnapshotInput {
	return c.SnapshotInput{OrderID: 10, OrderItemID: 11, ProductID: 1, SKUID: 1, ConfigVersion: 1, Quantity: 1}
}

// insert 使用 db 的外部事务插入引用 in；now 和身份由测试信任边界提供。
func insert(t *testing.T, db *gorm.DB, in c.SnapshotInput, now time.Time) (*d.OrderItemSnapshot, error) {
	t.Helper()
	var out *d.OrderItemSnapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		store, err := s.InTransaction(tx)
		if err != nil {
			return err
		}
		out, err = store.InsertSnapshot(context.Background(), in, identity(t), now)
		return err
	})
	return out, err
}

// TestSnapshotHistoryStable 验证编辑目标/期限不影响历史，重复写入不能覆盖快照。
func TestSnapshotHistoryStable(t *testing.T) {
	store, db, now := snapshotSetup(t)
	ctx := context.Background()
	if _, err := store.SaveConfig(ctx, input(), now); err != nil {
		t.Fatal(err)
	}
	original, err := insert(t, db, snapshotInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	in := input()
	in.ExpectedVersion = 1
	in.TargetIDs = []uint{2}
	in.Terms[0].Kind = d.TermPermanent
	in.Terms[0].Months = 0
	if _, err := store.SaveConfig(ctx, in, now); err != nil {
		t.Fatal(err)
	}
	actual, err := store.Snapshot(ctx, original.OrderItemID)
	if err != nil || !reflect.DeepEqual(original, actual) {
		t.Fatalf("history changed: %+v %v", actual, err)
	}
	if actual.Months != 2 || len(actual.Targets) != 2 || actual.BeneficiaryTelegramID != 9007199254740993 {
		t.Fatal("lost history or identity precision")
	}
	request := snapshotInput()
	request.ConfigVersion = 2
	if _, err := insert(t, db, request, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("immutable conflict: %v", err)
	}
	request.OrderItemID = 12
	permanent, err := insert(t, db, request, now)
	if err != nil || permanent.Kind != d.TermPermanent || permanent.Months != 0 || permanent.DurationSeconds != 0 {
		t.Fatalf("permanent: %+v %v", permanent, err)
	}
}

// TestSnapshotNeedsTrustedIdentityAndTransaction 验证 HTTP 不能解码身份、全局连接不能写快照。
func TestSnapshotNeedsTrustedIdentityAndTransaction(t *testing.T) {
	store, db, now := snapshotSetup(t)
	if _, err := s.InTransaction(db); !errors.Is(err, c.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if _, err := store.InsertSnapshot(context.Background(), snapshotInput(), identity(t), now); !errors.Is(err, c.ErrTransactionRequired) {
		t.Fatal(err)
	}
	var untrusted c.TrustedIdentity
	if err := json.Unmarshal([]byte(`{"accountID":7,"telegramID":99,"version":4,"AccountID":7}`), &untrusted); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := untrusted.Values(); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatal("HTTP identity populated")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		bound, err := s.InTransaction(tx)
		if err != nil {
			return err
		}
		_, err = bound.InsertSnapshot(context.Background(), snapshotInput(), untrusted, now)
		return err
	}); !errors.Is(err, c.ErrUntrustedIdentity) {
		t.Fatal(err)
	}
}

// TestOuterTransactionRollback 验证配置/快照与外部业务写入共享同一连接并一起回滚。
func TestOuterTransactionRollback(t *testing.T) {
	store, db, now := snapshotSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rollback := errors.New("simulated order failure")
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bound, err := s.InTransaction(tx)
		if err != nil {
			return err
		}
		if _, err := bound.SaveConfig(ctx, input(), now); err != nil {
			return err
		}
		if _, err := bound.InsertSnapshot(ctx, snapshotInput(), identity(t), now); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, err := store.Config(ctx, 1); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("configuration escaped transaction: %v", err)
	}
	if _, err := store.Snapshot(ctx, 11); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("snapshot escaped transaction: %v", err)
	}
}

// TestSnapshotRejectsStaleConfigAndQuantity 验证旧报价版本、重复数量和缺失 SKU 不生成快照。
func TestSnapshotRejectsStaleConfigAndQuantity(t *testing.T) {
	store, db, now := snapshotSetup(t)
	if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		mutate   func(*c.SnapshotInput)
		expected error
	}{
		{"stale", func(in *c.SnapshotInput) { in.ConfigVersion = 2 }, c.ErrConflict},
		{"quantity", func(in *c.SnapshotInput) { in.Quantity = 2 }, c.ErrInvalid},
		{"missing_sku", func(in *c.SnapshotInput) { in.SKUID = 99 }, c.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := snapshotInput()
			test.mutate(&in)
			if _, err := insert(t, db, in, now); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v got %v", test.expected, err)
			}
		})
	}
	if _, err := store.Snapshot(context.Background(), 11); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("rejected snapshot persisted: %v", err)
	}
}

// TestSnapshotRejectsForgedReferences 校验真实账号/订单项，而非仅信任内部传入的值对象。
func TestSnapshotRejectsForgedReferences(t *testing.T) {
	cases := map[string]func(*gorm.DB, *c.SnapshotInput) error{
		"order": func(_ *gorm.DB, in *c.SnapshotInput) error { in.OrderID = 9; return nil },
		"sku":   func(_ *gorm.DB, in *c.SnapshotInput) error { in.SKUID = 2; return nil },
		"account": func(db *gorm.DB, _ *c.SnapshotInput) error {
			return db.Model(&od.Order{}).Where("id = ?", 10).Update("user_id", 8).Error
		},
		"parent": func(db *gorm.DB, _ *c.SnapshotInput) error {
			return db.Model(&od.Order{}).Where("id = ?", 9).Update("user_id", 8).Error
		},
		"quantity": func(db *gorm.DB, _ *c.SnapshotInput) error {
			return db.Model(&od.OrderItem{}).Where("id = ?", 11).Update("quantity", 2).Error
		},
		"binding": func(db *gorm.DB, _ *c.SnapshotInput) error {
			return db.Model(&eid.Identity{}).Where("id = ?", 4).Update("user_id", 8).Error
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store, db, now := snapshotSetup(t)
			if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
				t.Fatal(err)
			}
			in := snapshotInput()
			if err := change(db, &in); err != nil {
				t.Fatal(err)
			}
			if _, err := insert(t, db, in, now); err == nil {
				t.Fatal("forged snapshot accepted")
			}
		})
	}
}

// TestCheckoutLedgerFailsClosed 验证默认订单事务端口不会把 B5 未实现当作没有永久资格。
func TestCheckoutLedgerFailsClosed(t *testing.T) {
	store, db, now := snapshotSetup(t)
	if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&d.ProductConfig{}).Where("product_id = 1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		checkout, err := s.CheckoutInTransaction(tx, nil)
		if err != nil {
			return err
		}
		_, err = checkout.Prepare(context.Background(), 7, []c.CheckoutLine{{ProductID: 1, SKUID: 1, Quantity: 1}}, now)
		return err
	})
	if !errors.Is(err, c.ErrOverlapUnavailable) {
		t.Fatalf("missing ledger must reject: %v", err)
	}
}

// TestMembershipDisplayUsesCurrentRoute verifies product bindings stay logical
// while their visible group name follows the new chat's matched observation.
func TestMembershipDisplayUsesCurrentRoute(t *testing.T) {
	store, db, now := snapshotSetup(t)
	if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
		t.Fatal(err)
	}
	config, err := store.Config(context.Background(), 1)
	if err != nil || len(config.Targets) == 0 {
		t.Fatal("missing product target", err)
	}
	target := config.Targets[0].TargetID
	if err := db.Model(&gd.Target{}).Where("id = ?", target).Updates(map[string]any{"telegram_chat_id": -9009, "route_version": 2, "obs_route_version": 2, "obs_title": "替换后的会员群"}).Error; err != nil {
		t.Fatal(err)
	}
	display, err := store.Display(context.Background(), 1)
	if err != nil || len(display.GroupNames) == 0 || display.GroupNames[0] != "替换后的会员群" {
		t.Fatalf("product still shows old group: %+v %v", display, err)
	}
	after, err := store.Config(context.Background(), 1)
	if err != nil || !reflect.DeepEqual(config, after) {
		t.Fatalf("product entitlement config mutated: %+v %v", after, err)
	}
}

// TestSavepointRollback 验证调用方捕获保存错误后，事务内不会留下已递增版本。
func TestSavepointRollback(t *testing.T) {
	store, db, now := snapshotSetup(t)
	ctx := context.Background()
	original, err := store.SaveConfig(ctx, input(), now)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		bound, err := s.InTransaction(tx)
		if err != nil {
			return err
		}
		in := input()
		in.ExpectedVersion = 1
		in.Terms[0].SKUID = 999
		if _, err := bound.SaveConfig(ctx, in, now); err == nil {
			return errors.New("expected rejected SKU")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.Config(ctx, 1)
	if err != nil || !reflect.DeepEqual(original, actual) {
		t.Fatalf("savepoint leaked partial update: %+v %v", actual, err)
	}
}
