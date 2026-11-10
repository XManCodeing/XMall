package application_test

import (
	"context"
	"errors"
	"fmt"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"sync"
	"testing"
	"time"
)

// lifecycleHarness checks that every platform call can acquire the sole SQLite connection.
type lifecycleHarness struct {
	t                        *testing.T
	db                       *gorm.DB
	store                    *s.Store
	service                  *a.Lifecycle
	route                    c.AccessRoute
	now                      time.Time
	status                   string
	restrictedMember         bool
	bans, unbans             int
	banOutcome, unbanOutcome string
	onBan                    func()
	onMember                 func()
	fence                    bool
}

// newLifecycleHarness creates an isolated expired historical member with no current binding table.
func newLifecycleHarness(t *testing.T) (*lifecycleHarness, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:life-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
			t.Error(err)
		}
	})
	models := append(s.LedgerModels(), s.AccessModels()...)
	models = append(models, s.LifecycleModels()...)
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE telegram_group_targets (id integer PRIMARY KEY,recovery_task_id integer NOT NULL DEFAULT 0)").Error; err != nil {
		t.Fatal(err)
	}
	h := &lifecycleHarness{t: t, db: db, store: s.New(db), now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), status: "member", banOutcome: "succeeded", unbanOutcome: "succeeded", fence: true, route: c.AccessRoute{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, Generation: 3}}
	h.seed(9007199254740993, 1)
	h.service, err = a.NewLifecycle(h.store, h, h)
	if err != nil {
		t.Fatal(err)
	}
	h.service.Now = func() time.Time { return h.now }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return h, r.WithExecutionFence(ctx, h)
}

// seed persists an expired ledger source and exact historical member ID for the same physical group.
func (h *lifecycleHarness) seed(actor int64, item uint) {
	h.t.Helper()
	source := d.LedgerSource{OrderItemID: item, TargetID: 1, BeneficiaryTelegramID: actor, AccountID: 7, OrderID: 1, Operation: "grant", Kind: d.TermMonths, Months: 1, PaidAt: h.now.Add(-31 * 24 * time.Hour), CreatedAt: h.now}
	if err := h.db.Create(&source).Error; err != nil {
		h.t.Fatal(err)
	}
	if err := h.db.Create(&d.LedgerProjection{TargetID: 1, BeneficiaryTelegramID: actor, Version: 1, CalculatedAt: h.now}).Error; err != nil {
		h.t.Fatal(err)
	}
	member := d.AccessMember{TargetID: 1, ChatID: -1001, TelegramID: actor, BotID: 9, RouteVersion: 1, SourceID: source.ID, Status: "member", IsMember: true, ObservedAt: h.now.Add(-time.Hour), ManagedAt: h.now.Add(-time.Hour), ObservationSource: "chat_member"}
	if err := h.db.Create(&member).Error; err != nil {
		h.t.Fatal(err)
	}
}

// MembershipRoute returns current capability-bound routing for this isolated test.
func (h *lifecycleHarness) MembershipRoute(context.Context, uint) (c.AccessRoute, error) {
	return h.route, nil
}

// Check rejects old runtime generations and simulated ownership loss.
func (h *lifecycleHarness) Check(_ context.Context, bot uint, generation uint64) error {
	if !h.fence || bot != h.route.BotID || generation != h.route.Generation {
		return r.ErrStale
	}
	return nil
}

// connection proves no transaction holds the single database connection during platform I/O.
func (h *lifecycleHarness) connection(ctx context.Context) {
	h.t.Helper()
	var count int64
	if err := h.db.WithContext(ctx).Model(&d.AccessOperation{}).Count(&count).Error; err != nil {
		h.t.Fatal(err)
	}
}

// GetMembershipMember returns exact restricted membership semantics without changing ban evidence.
func (h *lifecycleHarness) GetMembershipMember(ctx context.Context, input c.AccessJoinAction) c.AccessMemberResult {
	h.connection(ctx)
	if input.TelegramID != 9007199254740993 {
		h.t.Fatalf("wrong historical member %d", input.TelegramID)
	}
	if h.onMember != nil {
		h.onMember()
	}
	return c.AccessMemberResult{AccessAPIResult: c.AccessAPIResult{Outcome: "succeeded"}, Status: h.status, IsMember: h.status == "member" || h.status == "restricted" && h.restrictedMember}
}

// BanMembershipMember models a platform ban and an optional renewal or route change race.
func (h *lifecycleHarness) BanMembershipMember(ctx context.Context, _ c.AccessJoinAction) c.AccessAPIResult {
	h.connection(ctx)
	h.bans++
	h.status = "kicked"
	if h.onBan != nil {
		h.onBan()
	}
	return c.AccessAPIResult{Outcome: h.banOutcome}
}

// UnbanMembershipMember models only-if-banned behavior; present members are not removed.
func (h *lifecycleHarness) UnbanMembershipMember(ctx context.Context, _ c.AccessJoinAction) c.AccessAPIResult {
	h.connection(ctx)
	h.unbans++
	if h.unbanOutcome == "succeeded" && h.status == "kicked" {
		h.status = "left"
	}
	return c.AccessAPIResult{Outcome: h.unbanOutcome}
}

// run performs one real bounded scan then drains a bounded number of durable phases.
func (h *lifecycleHarness) run(ctx context.Context) {
	h.t.Helper()
	if _, err := h.service.Scan(ctx, 9, 100); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 20); err != nil {
		h.t.Fatal(err)
	}
}

// task reads the sole operation for phase assertions.
func (h *lifecycleHarness) task() d.AccessOperation {
	h.t.Helper()
	var row d.AccessOperation
	if err := h.db.First(&row, "kind = ?", "lifecycle").Error; err != nil {
		h.t.Fatal(err)
	}
	return row
}

// renew adds a second immutable source and updates the projection version like ledger grant.
func (h *lifecycleHarness) renew(kind string) {
	h.t.Helper()
	source := d.LedgerSource{OrderItemID: 99, TargetID: 1, BeneficiaryTelegramID: 9007199254740993, AccountID: 7, OrderID: 99, Operation: "grant", Kind: kind, Months: 1, PaidAt: h.now, CreatedAt: h.now}
	if kind == d.TermPermanent {
		source.Months = 0
	}
	if err := h.db.Create(&source).Error; err != nil {
		h.t.Fatal(err)
	}
	if err := h.db.Model(&d.LedgerProjection{}).Where("target_id = ?", 1).Update("version", 2).Error; err != nil {
		h.t.Fatal(err)
	}
}

func TestLifecycleSingleConnectionReplayAndHistoricalIdentity(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	h.run(ctx)
	row := h.task()
	if h.bans != 1 || h.unbans != 1 || row.State != "succeeded" || !row.BanConfirmed || row.BanOutcome != "succeeded" || row.UnbanOutcome != "succeeded" {
		t.Fatalf("bad lifecycle %+v bans=%d unbans=%d", row, h.bans, h.unbans)
	}
	h.run(ctx)
	if h.bans != 1 || h.unbans != 1 {
		t.Fatal("replayed side effects")
	}
}

func TestLifecycleRenewalDuringBanRestoresAdmissionWithoutTime(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	h.onBan = func() { h.renew(d.TermMonths) }
	h.run(ctx)
	row := h.task()
	if row.ReviewReason != "admission_restored" || h.bans != 1 || h.unbans != 1 {
		t.Fatalf("renewal not compensated %+v", row)
	}
	eligible, err := h.store.Eligibility(ctx, c.AccessIdentity{TelegramID: 9007199254740993}, 1, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if eligible.ExpiresAt == nil || !eligible.ExpiresAt.Equal(h.now.Add(30*24*time.Hour)) {
		t.Fatalf("compensation changed business time %+v", eligible)
	}
}

func TestLifecyclePermanentAndMemberSafety(t *testing.T) {
	for _, scenario := range []string{"permanent", "administrator", "creator", "left", "restricted_left", "restricted_member", "external_ban"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			switch scenario {
			case "permanent":
				h.renew(d.TermPermanent)
			case "administrator", "creator", "left":
				h.status = scenario
			case "restricted_left", "restricted_member":
				h.status = "restricted"
				h.restrictedMember = scenario == "restricted_member"
			case "external_ban":
				h.status = "kicked"
			}
			h.run(ctx)
			want := 0
			if scenario == "restricted_member" {
				want = 1
			}
			if h.bans != want || h.unbans != want {
				t.Fatalf("unsafe operations ban=%d unban=%d", h.bans, h.unbans)
			}
			if scenario == "administrator" || scenario == "creator" || scenario == "external_ban" {
				if h.task().State != "needs_review" {
					t.Fatal("missing review")
				}
			}
		})
	}
}

func TestLifecycleUnknownBanNeverInfersOwnership(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	h.banOutcome = "unknown"
	h.run(ctx)
	row := h.task()
	if row.BanConfirmed || row.State != "needs_review" || row.ReviewReason != "ban_attribution_unknown" || h.unbans != 0 {
		t.Fatalf("unsafe attribution %+v", row)
	}
	if err := h.service.Recover(ctx, c.LifecycleRecovery{BotID: 9, ID: row.ID, ExpectedVersion: row.Version, ActorAdminID: 1, Action: "retry"}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("unknown retry allowed %v", err)
	}
	if err := h.service.Recover(ctx, c.LifecycleRecovery{BotID: 9, ID: row.ID, ExpectedVersion: row.Version, ActorAdminID: 1, Action: "recheck"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	if h.bans != 1 || h.unbans != 0 {
		t.Fatal("unknown replayed")
	}
}

func TestLifecycleFairBoundedScanAndUnmanagedExclusion(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	for index := uint(2); index <= 8; index++ {
		h.seed(int64(100+index), index)
	}
	external := d.AccessMember{TargetID: 1, ChatID: -1001, TelegramID: 999, BotID: 9, RouteVersion: 1, Status: "member", IsMember: true, ObservedAt: h.now, ManagedAt: h.now}
	if err := h.db.Create(&external).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 6; index++ {
		count, err := h.service.Scan(ctx, 9, 2)
		if err != nil || count > 2 {
			t.Fatalf("unbounded scan %d %v", count, err)
		}
	}
	var rows []d.AccessOperation
	if err := h.db.Where("kind = ?", "lifecycle").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("starvation/unmanaged inclusion %d", len(rows))
	}
}

func TestLifecycleLostLeaseAndRouteGeneration(t *testing.T) {
	for _, scenario := range []string{"lost_lease", "route", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			h.onBan = func() {
				switch scenario {
				case "lost_lease":
					h.now = h.now.Add(2 * time.Minute)
				case "route":
					h.route.RouteVersion++
				case "generation":
					h.route.Generation++
				}
			}
			_, err := h.service.Scan(ctx, 9, 100)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.service.Run(ctx, 9, 10)
			if scenario == "lost_lease" && !errors.Is(err, c.ErrConflict) {
				t.Fatalf("lost lease accepted %v", err)
			}
			if scenario != "lost_lease" && err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.Run(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			if h.bans != 1 || h.unbans != 0 {
				t.Fatal("old fence replayed effects")
			}
			if h.task().State != "needs_review" {
				t.Fatalf("lost effect not reviewed %+v", h.task())
			}
		})
	}
}

func TestLifecycleConcurrentScannersAndClaims(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	var wait sync.WaitGroup
	failures := make(chan error, 8)
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := h.service.Scan(ctx, 9, 1); failures <- err }()
	}
	wait.Wait()
	for index := 0; index < 4; index++ {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	claims := make(chan *d.AccessOperation, 4)
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			row, err := h.store.ClaimLifecycle(ctx, 9, fmt.Sprintf("worker-token-%016d", index), h.now, time.Minute)
			if err != nil && !errors.Is(err, c.ErrNotFound) {
				failures <- err
			}
			claims <- row
		}(index)
	}
	wait.Wait()
	close(claims)
	count := 0
	for row := range claims {
		if row != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("multiple owners %d", count)
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}

// TestLifecyclePlatformSecondBanIntervals exercises real Observe -> Run ordering on one SQLite connection.
// External same-second events are ambiguous, older disjoint intervals and managed-bot actors are not.
func TestLifecyclePlatformSecondBanIntervals(t *testing.T) {
	cases := []struct {
		name            string
		confirmedOffset time.Duration
		eventOffset     time.Duration
		managedActor    bool
		blocked         bool
	}{
		{"external_same_second", 100 * time.Millisecond, 0, false, true},
		{"external_previous_second", 100 * time.Millisecond, -time.Second, false, false},
		{"external_interval_ends_at_confirmation", 0, -time.Second, false, false},
		{"managed_bot_same_second", 100 * time.Millisecond, 0, true, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			second := h.now
			h.now = second.Add(test.confirmedOffset)
			if _, err := h.service.Scan(ctx, 9, 100); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.Run(ctx, 9, 2); err != nil {
				t.Fatal(err)
			}
			before := h.task()
			if !before.BanConfirmed || before.BanConfirmedAt == nil || !before.BanConfirmedAt.Equal(h.now) || before.LifecyclePhase != "unban" {
				t.Fatalf("missing exact local acknowledgement: %+v", before)
			}
			var prior d.AccessMember
			if err := h.db.First(&prior).Error; err != nil {
				t.Fatal(err)
			}
			// Arrival at .800 does not change Telegram's integer-second event fact.
			h.now = second.Add(800 * time.Millisecond)
			observed := second.Add(test.eventOffset)
			changed, err := h.service.Observe(ctx, c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: 9007199254740993, Status: "kicked", ObservedAt: observed, Source: "chat_member", Sequence: 10, ActorIsManagedBot: test.managedActor})
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				t.Fatal("older event overwrote newer query watermark")
			}
			var after d.AccessMember
			if err := h.db.First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if after.Status != prior.Status || !after.ObservedAt.Equal(prior.ObservedAt) || after.Sequence != prior.Sequence {
				t.Fatal("member watermark changed")
			}
			row := h.task()
			if row.BanBlocked != test.blocked || !row.BanConfirmedAt.Equal(*before.BanConfirmedAt) {
				t.Fatalf("incorrect interval comparison: %+v", row)
			}
			if test.blocked && !row.UpdatedAt.Equal(observed) {
				t.Fatal("fabricated event timestamp")
			}
			if _, err := h.service.Run(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			row = h.task()
			if test.blocked {
				if h.unbans != 0 || row.State != "needs_review" || row.ReviewReason != "disciplinary_ban" {
					t.Fatalf("external ban removed: %+v", row)
				}
			} else if h.unbans != 1 || row.State != "succeeded" {
				t.Fatalf("safe own-ban cleanup blocked: %+v", row)
			}
			if h.bans != 1 {
				t.Fatal("ban replayed")
			}
		})
	}
}

func TestLifecycleKnownDisciplinaryBanBlocksUnban(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	if _, err := h.service.Scan(ctx, 9, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 2); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(time.Second)
	_, err := h.service.Observe(ctx, c.AccessObservation{TargetID: 1, BotID: 9, ChatID: -1001, RouteVersion: 1, TelegramID: 9007199254740993, Status: "kicked", ObservedAt: h.now, Source: "chat_member", Sequence: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	if h.unbans != 0 || !h.task().BanBlocked || h.task().State != "needs_review" {
		t.Fatal("disciplinary ban removed")
	}
}

// TestLifecycleRevocationRecalculatesEverySource ensures source cancellation preserves other grants.
func TestLifecycleRevocationRecalculatesEverySource(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	h.renew(d.TermPermanent)
	key := d.SourceKey{EntitlementScope: d.EntitlementScope{TargetID: 1, BeneficiaryTelegramID: 9007199254740993}, OrderItemID: 1}
	if err := h.store.RevokeSource(ctx, key, h.now); err != nil {
		t.Fatal(err)
	}
	h.run(ctx)
	if h.bans != 0 {
		t.Fatal("old cancellation kicked permanent member")
	}
	key.OrderItemID = 99
	if err := h.store.RevokeSource(ctx, key, h.now); err != nil {
		t.Fatal(err)
	}
	h.run(ctx)
	if h.bans != 1 || h.unbans != 1 {
		t.Fatal("explicit cancellation not applied")
	}
}

// TestLifecycleUnknownUnbanOnlyReads ensures unknown unban is never blindly retried.
func TestLifecycleUnknownUnbanOnlyReads(t *testing.T) {
	for _, status := range []string{"kicked", "left"} {
		t.Run(status, func(t *testing.T) {
			h, ctx := newLifecycleHarness(t)
			h.unbanOutcome = "unknown"
			if _, err := h.service.Scan(ctx, 9, 100); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.Run(ctx, 9, 3); err != nil {
				t.Fatal(err)
			}
			h.status = status
			if _, err := h.service.Run(ctx, 9, 10); err != nil {
				t.Fatal(err)
			}
			row := h.task()
			want := "needs_review"
			if status == "left" {
				want = "succeeded"
			}
			if row.State != want || row.UnbanOutcome != "unknown" || h.unbans != 1 {
				t.Fatalf("unknown unban replay %+v", row)
			}
		})
	}
}

// TestLifecycleNewBanAfterSuccessfulUnbanCannotReuseOwnership prevents lifting a new external ban.
func TestLifecycleNewBanAfterSuccessfulUnbanCannotReuseOwnership(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	if _, err := h.service.Scan(ctx, 9, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 3); err != nil {
		t.Fatal(err)
	}
	h.status = "kicked"
	if _, err := h.service.Run(ctx, 9, 10); err != nil {
		t.Fatal(err)
	}
	row := h.task()
	if row.State != "needs_review" || row.ReviewReason != "external_ban" || h.unbans != 1 {
		t.Fatalf("stale attribution %+v", row)
	}
	if err := h.service.Recover(ctx, c.LifecycleRecovery{BotID: 9, ID: row.ID, ExpectedVersion: row.Version, ActorAdminID: 1, Action: "retry"}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale retry %v", err)
	}
}

// TestLifecycleBanReceiptSurvivesLostLease retains actual response independently from task completion CAS.
func TestLifecycleBanReceiptSurvivesLostLease(t *testing.T) {
	h, ctx := newLifecycleHarness(t)
	h.onBan = func() { h.now = h.now.Add(2 * time.Minute) }
	if _, err := h.service.Scan(ctx, 9, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Run(ctx, 9, 10); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("expected lease loss %v", err)
	}
	var receipt d.AccessResult
	if err := h.db.Where("action = ?", "ban").First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != "succeeded" {
		t.Fatalf("response lost %+v", receipt)
	}
	if h.task().BanConfirmed {
		t.Fatal("stale owner completed")
	}
}
