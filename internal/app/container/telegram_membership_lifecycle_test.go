package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fd "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	fs "github.com/XManCodeing/XMall/internal/modules/fulfillment/infrastructure/gormstore"
	identity "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	refundapp "github.com/XManCodeing/XMall/internal/modules/order/application/refund"
	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	pd "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	groupcontract "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	rs "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// reminderDependencyFault injects one transient read failure after the message is already queued.
type reminderDependencyFault struct {
	c.ReminderRepository
	remaining atomic.Int32
}

// Reminder fails only the requested first read; subsequent attempts use the actual single connection.
func (store *reminderDependencyFault) Reminder(ctx context.Context, id uint) (*d.Reminder, error) {
	if store.remaining.Swap(0) > 0 {
		return nil, errors.New("synthetic temporary database failure")
	}
	return store.ReminderRepository.Reminder(ctx, id)
}

type reminderRouteFault struct {
	c.AccessRoutes
	remaining atomic.Int32
}

// MembershipRoute simulates a stale observation without changing the actual persisted route.
func (routes *reminderRouteFault) MembershipRoute(ctx context.Context, id uint) (c.AccessRoute, error) {
	if routes.remaining.Swap(0) > 0 {
		return c.AccessRoute{}, groupcontract.ErrCapability
	}
	return routes.AccessRoutes.MembershipRoute(ctx, id)
}

type reminderRetryHandler struct {
	reminderRuntimeHandler
	lost bool
}

// AuthorizeReminderDelivery uses the real guard with a deliberately lost execution fence when requested.
func (handler reminderRetryHandler) AuthorizeReminderDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	if handler.lost {
		ctx = runtime.WithExecutionFence(ctx, &membershipReconcileFence{lost: true})
	}
	return handler.bridge.AuthorizeReminderDelivery(ctx, bot, message)
}

// runReminderRetryWorker consumes the real queue, preserving its current available_at and priority.
func runReminderRetryWorker(t *testing.T, fixture *membershipAccessFixture, lost bool) int32 {
	t.Helper()
	if err := fixture.db.AutoMigrate(&rd.Inbox{}); err != nil {
		t.Fatal(err)
	}
	queue, api := rs.New(fixture.db), &reminderRuntimeAPI{}
	manager := runtimeapp.NewManager(queue, api, "synthetic-membership-assembly-key")
	cipher, err := manager.Seal("100:synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	var instance rd.Instance
	if err := fixture.db.First(&instance, 1).Error; err != nil {
		t.Fatal(err)
	}
	// A restart advances generation; never reuse a generation already recorded in the exit ledger.
	if err := fixture.db.Model(&groups.Target{}).Where("bot_instance_id = 1").Update("obs_generation", instance.Generation+1).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&rd.Instance{}).Where("id = 1").Updates(map[string]any{"token_cipher": cipher, "state": "running"}).Error; err != nil {
		t.Fatal(err)
	}
	worker := runtimeapp.NewRuntime(manager, reminderRuntimeLease{}, reminderRetryHandler{reminderRuntimeHandler: reminderRuntimeHandler{fixture}, lost: lost})
	ctx, cancel := context.WithTimeout(fixture.ctx, 350*time.Millisecond)
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	return api.sends.Load()
}

// TestMembershipReminderDependencyRetry proves queued dependency failures back off instead of losing a tier.
// Runtime workers send only through the synthetic API; every queue receipt is persisted on one connection.
func TestMembershipReminderDependencyRetry(t *testing.T) {
	for _, scenario := range []string{"route", "database", "expired", "renewed", "quiet", "fence", "unreachable"} {
		t.Run(scenario, func(t *testing.T) {
			// Anchor the business morning to the worker's current UTC date. The quiet
			// retry uses the following evening so its 08:00 deadline stays in the
			// wall-clock future even when this test starts near UTC midnight.
			morning := time.Now().UTC().Truncate(24 * time.Hour)
			fixture := reminderFixtureAt(t, true, morning)
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			original, _ := reminderDelivery(t, fixture)
			if scenario == "database" {
				fault := &reminderDependencyFault{ReminderRepository: fixture.bridge.Reminders.Store}
				fault.remaining.Store(1)
				fixture.bridge.Reminders.Store = fault
			} else {
				fault := &reminderRouteFault{AccessRoutes: fixture.bridge.Reminders.Routes}
				fault.remaining.Store(1)
				fixture.bridge.Reminders.Routes = fault
			}
			started := time.Now()
			if calls := runReminderRetryWorker(t, fixture, false); calls != 0 {
				t.Fatal("temporary failure sent", calls)
			}
			row, _ := reminderDelivery(t, fixture)
			if row.Status != "retry" || row.Attempts != 1 || row.AvailableAt.Before(started.Add(55*time.Second)) || row.Priority != original.Priority {
				t.Fatal("not durable bounded retry", row.Status, row.Attempts, row.AvailableAt, row.Priority)
			}
			if calls := runReminderRetryWorker(t, fixture, false); calls != 0 {
				t.Fatal("backoff bypassed")
			}
			row, _ = reminderDelivery(t, fixture)
			if row.Attempts != 1 {
				t.Fatal("busy retry", row.Attempts)
			}
			want := "sent"
			switch scenario {
			case "expired":
				fixture.bridge.Reminders.Now = func() time.Time { return morning.Add(8 * 24 * time.Hour) }
				want = "superseded"
			case "renewed":
				if err := fixture.db.Model(&d.LedgerProjection{}).Where("target_id = 2").Update("version", 2).Error; err != nil {
					t.Fatal(err)
				}
				want = "superseded"
			case "quiet":
				fixture.bridge.Reminders.Now = func() time.Time { return morning.Add(38 * time.Hour) }
				want = "retry"
			case "fence":
				want = "retry"
			case "unreachable":
				if err := fixture.db.Model(&rd.Contact{}).Where("bot_id = 1").Update("reachable", false).Error; err != nil {
					t.Fatal(err)
				}
				want = "unreachable"
			}
			// Move only the test scheduler deadline; a real restart preserves all other delivery fields.
			if err := fixture.db.Model(&rd.Delivery{}).Where("id = ?", row.ID).Update("available_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			calls := runReminderRetryWorker(t, fixture, scenario == "fence")
			row, _ = reminderDelivery(t, fixture)
			if row.Status != want || row.Priority != 20 || (want == "sent" && (calls != 1 || row.MessageID != 654)) || (want != "sent" && calls != 0) {
				t.Fatal("retry revalidation failed", row.Status, want, calls)
			}
			if scenario == "quiet" && !row.AvailableAt.Equal(morning.Add(48*time.Hour)) {
				t.Fatal("quiet deadline lost", row.AvailableAt)
			}
		})
	}
}

// delayedRefundFixture pauses consumption after persisting an authentic payment event on the real single connection.
// Returns fixture, transactional ledger and worker; sources are absent until worker resumes.
func delayedRefundFixture(t *testing.T) (*membershipAccessFixture, *memberstore.Store, *app.PaidWorker) {
	t.Helper()
	fixture := newMembershipAccessFixture(t, false)
	db := fixture.db
	if err := db.AutoMigrate(&d.PaidEvent{}, &d.LedgerOutbox{}, &od.OrderRefundRecord{}, &pd.Payment{}, &fd.Fulfillment{}, &fd.ItemReceipt{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&d.LedgerSource{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot d.OrderItemSnapshot
	if err := db.First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	snapshot.Targets = append(snapshot.Targets, d.SnapshotTarget{TargetID: 3, BotInstanceID: 1, TelegramChatID: -1002, RouteVersion: 1})
	if err := db.Save(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&od.Order{}).Where("id = 1").Updates(map[string]any{"total_amount": 10, "online_paid_amount": 10, "created_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	var order od.Order
	if err := db.First(&order, 1).Error; err != nil {
		t.Fatal(err)
	}
	payment := pd.Payment{ID: 1, OrderID: 1, Status: constants.PaymentStatusSuccess, PaidAt: order.PaidAt, Currency: "CNY", Amount: money.FromDecimal(decimal.NewFromInt(10))}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		bound, err := memberstore.InTransaction(tx)
		if err != nil {
			return err
		}
		return bound.AppendPaid(fixture.ctx, 1, 1)
	}); err != nil {
		t.Fatal(err)
	}
	store := memberstore.New(db).WithSystemFulfillment(func(tx *gorm.DB) fc.MembershipSystemStore { return fs.New(tx) })
	worker, err := app.NewPaidWorker(store, store, time.Now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, store, worker
}

// TestMembershipRefundBeforePaidWorker covers partial/full refunds with and without explicit withdrawal.
func TestMembershipRefundBeforePaidWorker(t *testing.T) {
	for _, amount := range []int64{1, 10} {
		for _, dispose := range []bool{false, true} {
			name := decimal.NewFromInt(amount).String()
			if dispose {
				name += "_disposed"
			}
			t.Run(name, func(t *testing.T) {
				fixture, store, worker := delayedRefundFixture(t)
				refunds := refundapp.New(orderstore.New(fixture.db, "synthetic-refund-pause"), nil, nil, nil, nil, nil)
				if _, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 1, Amount: money.FromDecimal(decimal.NewFromInt(amount))}); err != nil {
					t.Fatal(err)
				}
				candidates, err := store.RefundCandidates(fixture.ctx, 1)
				if err != nil || len(candidates) != 1 {
					t.Fatal("candidate missing", err)
				}
				input := candidates[0].RefundSelection
				input.ActorAdminID = 3
				if len(input.SourceIDs) != 0 {
					t.Fatal("source fabricated")
				}
				if dispose {
					for range 2 {
						if err := store.DisposeRefund(fixture.ctx, input); err != nil {
							t.Fatal(err)
						}
					}
					countMembershipRows(t, fixture.db, &d.LedgerRevocation{}, 2)
					countMembershipRows(t, fixture.db, &d.LedgerSource{}, 0)
					countMembershipRows(t, fixture.db, &d.RefundDisposition{}, 1)
				}
				if count, err := worker.RunOnce(fixture.ctx, 10); err != nil || count != 1 {
					t.Fatal("worker", count, err)
				}
				countMembershipRows(t, fixture.db, &d.LedgerSource{}, 2)
				receiptCount := int64(1)
				if dispose {
					receiptCount = 0
				}
				countMembershipRows(t, fixture.db, &fd.ItemReceipt{}, receiptCount)
				for _, target := range []uint{2, 3} {
					projection, err := store.Recalculate(fixture.ctx, d.EntitlementScope{TargetID: target, BeneficiaryTelegramID: membershipAssemblyTelegramID}, fixture.now)
					if err != nil || projection.Active == dispose {
						t.Fatal("wrong eligibility", projection, err)
					}
				}
				var order od.Order
				if err := fixture.db.First(&order, 1).Error; err != nil {
					t.Fatal(err)
				}
				expected := constants.OrderStatusPartiallyRefunded
				if amount == 10 {
					expected = constants.OrderStatusRefunded
				}
				if order.Status != expected {
					t.Fatal("refund overwritten", order.Status)
				}
				if dispose {
					if err := store.DisposeRefund(fixture.ctx, input); err != nil {
						t.Fatal("late replay", err)
					}
				}
			})
		}
	}
}

// TestMembershipSiblingRefundBeforeWorker keeps ordinary child refunds outside membership policy.
func TestMembershipSiblingRefundBeforeWorker(t *testing.T) {
	fixture, store, worker := delayedRefundFixture(t)
	root := uint(1)
	var parent od.Order
	if err := fixture.db.First(&parent, root).Error; err != nil {
		t.Fatal(err)
	}
	for _, order := range []od.Order{
		{ID: 2, ParentID: &root, UserID: 7, OrderNo: "member-child", Status: constants.OrderStatusPaid, PaidAt: parent.PaidAt, CreatedAt: fixture.now, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(5))},
		{ID: 3, ParentID: &root, UserID: 7, OrderNo: "ordinary-child", Status: constants.OrderStatusPaid, PaidAt: parent.PaidAt, CreatedAt: fixture.now, Currency: "CNY", TotalAmount: money.FromDecimal(decimal.NewFromInt(5))},
	} {
		if err := fixture.db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Model(&od.OrderItem{}).Where("id = 1").Update("order_id", 2).Error; err != nil {
		t.Fatal(err)
	}
	refunds := refundapp.New(orderstore.New(fixture.db, "synthetic-refund-sibling"), nil, nil, nil, nil, nil)
	if _, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 3, Amount: money.FromDecimal(decimal.NewFromInt(1))}); err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(fixture.ctx, 10); err != nil || count != 1 {
		t.Fatal("sibling blocked grant", count, err)
	}
	rows, err := store.RefundCandidates(fixture.ctx, 1)
	if err != nil || len(rows) != 0 {
		t.Fatal("ordinary refund attributed to member", err)
	}
	countMembershipRows(t, fixture.db, &fd.ItemReceipt{}, 1)
	countMembershipRows(t, fixture.db, &d.LedgerRevocation{}, 0)
}

// TestMembershipUnpostedRefundRejectsFakePayment rejects a refund without genuine payment evidence.
func TestMembershipUnpostedRefundRejectsFakePayment(t *testing.T) {
	for _, column := range []string{"status", "exception_code", "order_id"} {
		t.Run(column, func(t *testing.T) {
			fixture, store, _ := delayedRefundFixture(t)
			refunds := refundapp.New(orderstore.New(fixture.db, "synthetic-refund-evidence"), nil, nil, nil, nil, nil)
			if _, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 1, Amount: money.FromDecimal(decimal.NewFromInt(1))}); err != nil {
				t.Fatal(err)
			}
			values := map[string]any{"status": "pending", "exception_code": "underpaid_succeeded", "order_id": 99}
			if err := fixture.db.Model(&pd.Payment{}).Where("id = 1").Update(column, values[column]).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.RefundCandidates(fixture.ctx, 1); err == nil {
				t.Fatal("untrusted payment accepted")
			}
			countMembershipRows(t, fixture.db, &d.LedgerRevocation{}, 0)
		})
	}
}

// TestMembershipUnpostedRefundRollback rolls back every target tombstone when the last audit write fails.
func TestMembershipUnpostedRefundRollback(t *testing.T) {
	fixture, store, _ := delayedRefundFixture(t)
	refunds := refundapp.New(orderstore.New(fixture.db, "synthetic-refund-rollback"), nil, nil, nil, nil, nil)
	if _, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 1, Amount: money.FromDecimal(decimal.NewFromInt(1))}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.RefundCandidates(fixture.ctx, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatal(err)
	}
	input := candidates[0].RefundSelection
	input.ActorAdminID = 3
	if err := fixture.db.Exec("CREATE TRIGGER reject_early_refund_audit BEFORE INSERT ON telegram_membership_refund_dispositions BEGIN SELECT RAISE(ABORT, 'audit rejected'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DisposeRefund(fixture.ctx, input); err == nil {
		t.Fatal("audit failure accepted")
	}
	countMembershipRows(t, fixture.db, &d.LedgerRevocation{}, 0)
	countMembershipRows(t, fixture.db, &d.RefundDisposition{}, 0)
	countMembershipRows(t, fixture.db, &d.LedgerOutbox{}, 0)
	rows, err := store.RefundCandidates(fixture.ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ExpectedVersion != input.ExpectedVersion {
		t.Fatal("notice version escaped rollback", err)
	}
}

// reminderRuntimeHandler retains the real membership guard while excluding unrelated shop UI.
type reminderRuntimeHandler struct{ *membershipAccessFixture }

// Handle delegates trusted events to the actual bridge.
func (handler reminderRuntimeHandler) Handle(ctx context.Context, bot uint, update runtime.Update) ([]runtime.Message, error) {
	return handler.bridge.Handle(ctx, bot, update)
}

// AuthorizeDelivery leaves exact binding validation to the membership guard.
func (reminderRuntimeHandler) AuthorizeDelivery(context.Context, uint, int64, uint) error { return nil }

// AuthorizeReminderDelivery forwards actual persisted authorization validation.
func (handler reminderRuntimeHandler) AuthorizeReminderDelivery(ctx context.Context, bot uint, message runtime.Message) error {
	return handler.bridge.AuthorizeReminderDelivery(ctx, bot, message)
}

type reminderRuntimeLease struct{}

func (reminderRuntimeLease) Renew(context.Context) error   { return nil }
func (reminderRuntimeLease) Release(context.Context) error { return nil }
func (reminderRuntimeLease) Acquire(context.Context, string) (runtime.Lease, error) {
	return reminderRuntimeLease{}, nil
}

type reminderRuntimeAPI struct {
	runtime.API
	sends atomic.Int32
}

func (*reminderRuntimeAPI) Webhook(context.Context, string) (string, error) { return "", nil }
func (*reminderRuntimeAPI) Poll(ctx context.Context, _ string, _ int64) ([]runtime.Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (api *reminderRuntimeAPI) Send(context.Context, string, runtime.Message) (int64, error) {
	api.sends.Add(1)
	return 654, nil
}

// TestMembershipReminderRealWorker joins actual Runtime.Start, container reconciliation and one-connection receipts.
func TestMembershipReminderRealWorker(t *testing.T) {
	fixture := reminderFixture(t, true)
	if err := fixture.db.AutoMigrate(&rd.Inbox{}); err != nil {
		t.Fatal(err)
	}
	queue := rs.New(fixture.db)
	api := &reminderRuntimeAPI{}
	manager := runtimeapp.NewManager(queue, api, "synthetic-membership-assembly-key")
	cipher, err := manager.Seal("100:synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&rd.Instance{}).Where("id = 1").Updates(map[string]any{"token_cipher": cipher, "generation": 6}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.bridge.Reminders.Now = func() time.Time { return time.Now().UTC() }
	expiry := time.Now().UTC().Add(6 * 24 * time.Hour)
	if err := fixture.db.Model(&d.LedgerSource{}).Where("id = 1").Update("paid_at", expiry.Add(-30*24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	fixture.bridge.Lifecycle.(*app.Lifecycle).Now = fixture.bridge.Reminders.Now
	worker := runtimeapp.NewRuntime(manager, reminderRuntimeLease{}, reminderRuntimeHandler{fixture})
	worker.Reconcile = fixture.bridge.Reconcile
	ctx, cancel := context.WithTimeout(fixture.ctx, 350*time.Millisecond)
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	row, _ := reminderDelivery(t, fixture)
	if d.ReminderAvailable(time.Now()).After(time.Now()) {
		if api.sends.Load() != 0 || row.Status != "pending" {
			t.Fatal("quiet worker sent", row.Status)
		}
	} else if api.sends.Load() != 1 || row.Status != "sent" || row.MessageID != 654 {
		t.Fatal("no actual runtime receipt", api.sends.Load(), row.Status, row.MessageID)
	}
}

// TestMembershipReminderFairTargetScan checks bounded cursor wrap and per-target unique tiers.
func TestMembershipReminderFairTargetScan(t *testing.T) {
	fixture := reminderFixture(t, true)
	var source d.LedgerSource
	var projection d.LedgerProjection
	var target groups.Target
	if err := fixture.db.First(&source, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&projection).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&target, 2).Error; err != nil {
		t.Fatal(err)
	}
	source.ID, source.TargetID = 0, 3
	projection.TargetID = 3
	target.ID, target.TelegramChatID, target.OccupancySlot = 3, -1002, "-1002"
	for _, row := range []any{&target, &source, &projection} {
		if err := fixture.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	store, now := fixture.bridge.Reminders.Store, fixture.bridge.Reminders.Now()
	for iteration := 0; iteration < 7; iteration++ {
		scanned, err := store.ScanReminders(fixture.ctx, 1, 1, now)
		if err != nil || scanned > 1 {
			t.Fatal(scanned, err)
		}
	}
	countMembershipRows(t, fixture.db, &d.Reminder{}, 6)
	var notices []d.Reminder
	if err := fixture.db.Order("target_id, days").Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	for _, notice := range notices {
		if notice.TargetID != 2 && notice.TargetID != 3 {
			t.Fatal(notice)
		}
		if notice.Days != 7 && notice.Days != 3 && notice.Days != 1 {
			t.Fatal(notice)
		}
	}
	if err := fixture.db.Model(&d.LedgerProjection{}).Where("target_id = 2").Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := store.ScanReminders(fixture.ctx, 1, 1, now); err != nil {
			t.Fatal(err)
		}
	}
	countMembershipRows(t, fixture.db, &d.Reminder{}, 9)
}

// reminderFixture returns real container wiring backed by one connection and Shanghai morning.
func reminderFixture(t *testing.T, reachable bool) *membershipAccessFixture {
	t.Helper()
	return reminderFixtureAt(t, reachable, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
}

// reminderFixtureAt returns the real single-connection fixture at business time now.
// reachable controls the synthetic contact; source and projection expiry share now's seven-day horizon.
func reminderFixtureAt(t *testing.T, reachable bool, now time.Time) *membershipAccessFixture {
	t.Helper()
	fixture := newMembershipAccessFixture(t, true)
	fixture.bridge.Reminders.Now = func() time.Time { return now }
	expiry := now.Add(7 * 24 * time.Hour)
	paid := expiry.Add(-30 * 24 * time.Hour)
	for model, values := range map[any]map[string]any{
		&d.LedgerSource{}:     {"kind": d.TermMonths, "months": 1, "paid_at": paid},
		&d.LedgerProjection{}: {"kind": d.TermMonths, "expires_at": expiry},
	} {
		if err := fixture.db.Model(model).Where("1 = 1").Updates(values).Error; err != nil {
			t.Fatal(err)
		}
	}
	if reachable {
		if err := fixture.db.Create(&rd.Contact{BotID: 1, TelegramUserID: membershipAssemblyTelegramID, ChatID: membershipAssemblyTelegramID, UserID: 7, Reachable: true, Locale: "en-US"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

// reminderDelivery decodes synthetic encrypted payload to verify the original runtime authorization reference.
func reminderDelivery(t *testing.T, fixture *membershipAccessFixture) (rd.Delivery, runtime.Message) {
	t.Helper()
	var row rd.Delivery
	if err := fixture.db.First(&row, "purpose = ?", "membership_reminder").Error; err != nil {
		t.Fatal(err)
	}
	raw, err := fixture.bridge.Reminders.Cipher.Open(row.PayloadCipher)
	if err != nil {
		t.Fatal(err)
	}
	var message runtime.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	return row, message
}

// TestMembershipReminderContainerReceipt proves real reconciliation queues once and enqueue never means sent.
func TestMembershipReminderContainerReceipt(t *testing.T) {
	for _, outcome := range []string{"sent", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			fixture := reminderFixture(t, true)
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			row, message := reminderDelivery(t, fixture)
			if row.Priority >= 200 || message.ReminderDelivery == nil {
				t.Fatal("not a lower-priority reminder")
			}
			var notice d.Reminder
			if err := fixture.db.First(&notice, message.ReminderDelivery.ID).Error; err != nil || notice.State != "queued" {
				t.Fatal(notice, err)
			}
			if err := fixture.bridge.AuthorizeReminderDelivery(fixture.ctx, 1, message); err != nil {
				t.Fatal(err)
			}
			queue := rs.New(fixture.db)
			claim, err := queue.ClaimDeliveryExcluding(fixture.ctx, 1, 7, notice.NextAttemptAt.Add(time.Minute), nil)
			if err != nil || claim == nil {
				t.Fatal(claim, err)
			}
			receipt := int64(0)
			if outcome == "sent" {
				receipt = 123
			}
			if err := queue.FinishDelivery(fixture.ctx, claim.ID, claim.Claim, outcome, receipt, "", time.Now()); err != nil {
				t.Fatal(err)
			}
			fixture.bridge.Reminders.Queue = rs.New(fixture.db)
			fixture.bridge.Reminders.Now = func() time.Time { return notice.NextAttemptAt.Add(2 * time.Minute) }
			for range 2 {
				if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.db.First(&notice, notice.ID).Error; err != nil || notice.State != outcome || notice.MessageID != receipt {
				t.Fatal(notice, err)
			}
			countMembershipRows(t, fixture.db, &rd.Delivery{}, 1)
			countMembershipRows(t, fixture.db, &d.Reminder{}, 3)
		})
	}
}

// TestMembershipReminderQuietBoundary proves a queued message crossing Shanghai 22:00 waits until 08:00.
func TestMembershipReminderQuietBoundary(t *testing.T) {
	fixture := reminderFixture(t, true)
	quiet := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	fixture.bridge.Reminders.Now = func() time.Time { return quiet }
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	row, message := reminderDelivery(t, fixture)
	want := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	if !row.AvailableAt.Equal(want) {
		t.Fatal(row.AvailableAt)
	}
	if _, ok := fixture.bridge.AuthorizeReminderDelivery(fixture.ctx, 1, message).(runtime.DeliveryDeferred); !ok {
		t.Fatal("quiet delivery not deferred")
	}
	fixture.bridge.Reminders.Now = func() time.Time { return want }
	if err := fixture.bridge.AuthorizeReminderDelivery(fixture.ctx, 1, message); err != nil {
		t.Fatal(err)
	}
}

// TestMembershipReminderPreSendInvalidation rejects expired, renewed, rebound and rerouted notices.
func TestMembershipReminderPreSendInvalidation(t *testing.T) {
	for _, scenario := range []string{"expired", "renewed", "permanent", "binding", "route", "unreachable", "fence"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := reminderFixture(t, true)
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			_, message := reminderDelivery(t, fixture)
			var err error
			switch scenario {
			case "expired":
				fixture.bridge.Reminders.Now = func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) }
			case "renewed":
				err = fixture.db.Model(&d.LedgerProjection{}).Where("target_id = 2").Update("version", 2).Error
			case "permanent":
				err = fixture.db.Model(&d.LedgerSource{}).Where("id = 1").Update("kind", d.TermPermanent).Error
			case "binding":
				err = fixture.db.Model(&identity.Identity{}).Where("id = 1").Update("provider_user_id", "1234").Error
			case "route":
				err = fixture.db.Model(&groups.Target{}).Where("id = 2").Update("route_version", 2).Error
			case "unreachable":
				err = fixture.db.Model(&rd.Contact{}).Where("bot_id = 1").Update("reachable", false).Error
			case "fence":
				fixture.ctx = runtime.WithExecutionFence(fixture.ctx, &membershipReconcileFence{lost: true})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.bridge.AuthorizeReminderDelivery(fixture.ctx, 1, message); err == nil {
				t.Fatal("obsolete reminder authorized")
			}
		})
	}
}

// TestMembershipReminderUnreachablePermanent verifies absent contacts stay absent and permanent rights have no reminders.
func TestMembershipReminderUnreachablePermanent(t *testing.T) {
	fixture := reminderFixture(t, false)
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	var notice d.Reminder
	if err := fixture.db.First(&notice).Error; err != nil || notice.State != "unreachable" {
		t.Fatal(notice, err)
	}
	countMembershipRows(t, fixture.db, &rd.Contact{}, 0)
	countMembershipRows(t, fixture.db, &rd.Delivery{}, 0)
	permanent := newMembershipAccessFixture(t, false)
	if err := permanent.bridge.Reconcile(permanent.ctx, 1); err != nil {
		t.Fatal(err)
	}
	countMembershipRows(t, permanent.db, &d.Reminder{}, 0)
}

// TestMembershipRefundContainer uses the existing manual refund service, real transaction event and runtime consumer.
// Telegram alone is simulated; its adapter probes the single connection on every call.
func TestMembershipRefundContainer(t *testing.T) {
	fixture := newMembershipAccessFixture(t, false)
	if err := fixture.db.AutoMigrate(&od.OrderRefundRecord{}, &d.LedgerOutbox{}, &fd.Fulfillment{}); err != nil {
		t.Fatal(err)
	}
	var snapshot d.OrderItemSnapshot
	if err := fixture.db.First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if err := fixture.db.Model(&d.LedgerSource{}).Where("id = 1").Updates(map[string]any{"snapshot_hash": hex.EncodeToString(digest[:]), "order_id": 1, "payment_order_id": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&od.Order{}).Where("id = 1").Updates(map[string]any{"total_amount": 10, "created_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	lifecycle := fixture.bridge.Lifecycle.(*app.Lifecycle)
	platform := &lifecycleAssemblyPlatform{fixture: fixture, t: t, status: "member"}
	lifecycle.Platform = platform
	if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.memberUpdate(membershipAssemblyTelegramID)); err != nil {
		t.Fatal(err)
	}
	orders := orderstore.New(fixture.db, "synthetic-refund-container")
	refunds := refundapp.New(orders, nil, nil, nil, nil, nil)
	_, record, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 1, Amount: money.FromDecimal(decimal.NewFromInt(1)), Remark: "synthetic offline refund confirmed"})
	if err != nil || record == nil {
		t.Fatal("manual refund", err)
	}
	countMembershipRows(t, fixture.db, &d.RefundNotice{}, 1)
	countMembershipRows(t, fixture.db, &d.LedgerRevocation{}, 0)
	store := memberstore.New(fixture.db)
	candidates, err := store.RefundCandidates(fixture.ctx, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatal("candidate", err)
	}
	input := candidates[0].RefundSelection
	input.ActorAdminID = 3
	for range 2 {
		if err := store.DisposeRefund(fixture.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if platform.bans != 0 {
		t.Fatal("button performed kick")
	}
	if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if platform.bans != 1 || platform.unbans != 1 {
		t.Fatalf("runtime effects %d/%d", platform.bans, platform.unbans)
	}
	tasks, err := lifecycle.Tasks(fixture.ctx, 1, 0, 20)
	if err != nil || len(tasks) != 1 || tasks[0].ReviewReason != "removed" {
		t.Fatal("actual confirmation", tasks, err)
	}
	// Event failure aborts the funding fact too, never leaves a half-committed manual refund.
	if err := fixture.db.Callback().Create().Before("gorm:create").Register("refund:event_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_membership_refund_notices" {
			tx.AddError(c.ErrConflict)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := refunds.AdminManualRefund(refundapp.AdminManualRefundInput{OrderID: 1, Amount: money.FromDecimal(decimal.NewFromInt(1))}); err == nil {
		t.Fatal("event failure swallowed")
	}
	countMembershipRows(t, fixture.db, &oc.RefundFact{}, 1)
}

// lifecycleAssemblyPlatform checks that all external work can acquire the single database connection.
type lifecycleAssemblyPlatform struct {
	fixture *membershipAccessFixture
	t       *testing.T
	status  string
	bans    int
	unbans  int
}

// GetMembershipMember returns the simulated current platform state outside any transaction.
func (platform *lifecycleAssemblyPlatform) GetMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessMemberResult {
	var count int64
	if err := platform.fixture.db.WithContext(ctx).Model(&d.AccessOperation{}).Count(&count).Error; err != nil {
		platform.t.Fatal("network adapter called inside transaction", err)
	}
	return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: c.AccessSucceeded}, Status: platform.status, IsMember: platform.status == "member"}
}

// BanMembershipMember acknowledges a synthetic ban for the persisted historical identity.
func (platform *lifecycleAssemblyPlatform) BanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	platform.GetMembershipMember(ctx, input)
	platform.bans++
	platform.status = "kicked"
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// UnbanMembershipMember acknowledges safe synthetic restoration, without any real Telegram call.
func (platform *lifecycleAssemblyPlatform) UnbanMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessAPIResult {
	platform.GetMembershipMember(ctx, input)
	platform.unbans++
	platform.status = "left"
	return c.AccessAPIResult{Outcome: c.AccessSucceeded}
}

// TestMembershipLifecycleContainerConsumer exercises real container assembly, trusted observation and bounded task consumption.
func TestMembershipLifecycleContainerConsumer(t *testing.T) {
	for _, restricted := range []bool{true, false} {
		t.Run(map[bool]string{true: "authorized", false: "invite_only"}[restricted], func(t *testing.T) {
			fixture := newMembershipAccessFixture(t, false)
			lifecycle, ok := fixture.bridge.Lifecycle.(*app.Lifecycle)
			if !ok || fixture.bridge.Bots == nil {
				t.Fatal("container did not assemble lifecycle and bot identity")
			}
			platform := &lifecycleAssemblyPlatform{fixture: fixture, t: t, status: "member"}
			lifecycle.Platform = platform
			if _, err := fixture.bridge.Handle(fixture.ctx, 1, fixture.memberUpdate(membershipAssemblyTelegramID)); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&d.LedgerSource{}).Where("id = ?", 1).Updates(map[string]any{"kind": d.TermMonths, "months": 1, "paid_at": fixture.now.Add(-60 * 24 * time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
			if !restricted {
				if err := fixture.db.Exec("UPDATE telegram_group_targets SET obs_can_restrict_members = ?", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.bridge.Reconcile(fixture.ctx, 1); err != nil {
				t.Fatal(err)
			}
			if restricted && (platform.bans != 1 || platform.unbans != 1) || !restricted && (platform.bans != 0 || platform.unbans != 0) {
				t.Fatalf("unexpected effects: ban=%d unban=%d", platform.bans, platform.unbans)
			}
			countMembershipRows(t, fixture.db, &d.LifecycleCursor{}, 1)
		})
	}
}
