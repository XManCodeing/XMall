package telegramidentityrecovery

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	mappings "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestCancelAfterInviteRetirement keeps unknown cleanup evidence while releasing pre-mapping scope.
func TestCancelAfterInviteRetirement(t *testing.T) {
	db, executor, input := fixture(t)
	cancellation := confirmedCancellation(t, executor, input)
	ctx := context.Background()
	if err := db.AutoMigrate(&md.InviteCleanupItem{}); err != nil {
		t.Fatal(err)
	}
	invite := md.AccessInvite{ID: 11, TargetID: 1, TelegramID: 9007199254740993, State: "unknown"}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&md.InviteCleanupItem{CycleID: 1, InviteID: invite.ID, State: "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := receipt(ctx, tasks.New(db), cancellation.TaskID, "identity_invites_retirement_requested"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE telegram_group_targets SET route_version=12").Error; err != nil {
		t.Fatal(err)
	}
	cancelled, err := executor.Cancel(ctx, cancellation)
	if err != nil || cancelled.State != d.Cancelled {
		t.Fatalf("cancel=%+v %v", cancelled, err)
	}
	if _, err := executor.Cancel(ctx, cancellation); err != nil {
		t.Fatal(err)
	}
	stored, err := memberstore.New(db).Invite(ctx, invite.ID)
	if err != nil || !stored.Retired || stored.State != "unknown" {
		t.Fatalf("unsafe invite restoration=%+v %v", stored, err)
	}
	if md.IsInviteUsableAt(*stored, time.Now()) {
		t.Fatal("unknown retired invite became usable")
	}
	var slots int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", cancelled.ID).Count(&slots).Error; err != nil || slots != 0 {
		t.Fatalf("slots=%d %v", slots, err)
	}
}

// TestIdentityLongTermInviteRetirement persists cleanup before mappings and survives restarts.
// Simulated runtime receipts never perform real Telegram calls; unknown is never replayed.
func TestIdentityLongTermInviteRetirement(t *testing.T) {
	for _, scenario := range []string{"limited", "permanent", "failed", "unknown", "approval_unknown"} {
		t.Run(scenario, func(t *testing.T) {
			db, executor, input := fixture(t)
			if err := db.AutoMigrate(&md.InviteCleanupCycle{}, &md.InviteCleanupItem{}, &md.AccessResult{}); err != nil {
				t.Fatal(err)
			}
			if scenario == "permanent" {
				if err := db.Model(&md.LedgerSource{}).Where("id = 9").Updates(map[string]any{"kind": md.TermPermanent, "months": 0}).Error; err != nil {
					t.Fatal(err)
				}
			}
			invite := md.AccessInvite{ID: 1, AccountID: 7, TelegramID: 9007199254740993, TargetID: 1, BotID: 5, ChatID: -10001, RouteVersion: 11, SourceID: 9, Term: md.InviteTermLongTerm, State: "active", LinkCipher: "synthetic-encrypted", LinkHash: "0123456789012345678901234567890123456789012345678901234567890123"}
			if err := db.Create(&invite).Error; err != nil {
				t.Fatal(err)
			}
			confirmed := confirmedCancellation(t, executor, input)
			if scenario == "approval_unknown" {
				if err := db.Create(&md.AccessOperation{BusinessKey: "unknown-approval", Kind: "approve", TargetID: 1, TelegramID: invite.TelegramID, State: "unknown"}).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := executor.Resume(context.Background(), confirmed.TaskID, confirmed.ExpectedVersion, 3); !errors.Is(err, m.ErrConflict) {
					t.Fatalf("unknown approval bypassed: %v", err)
				}
				return
			}
			task, err := executor.Resume(context.Background(), confirmed.TaskID, confirmed.ExpectedVersion, 3)
			if err != nil || task.State != d.Fencing {
				t.Fatalf("cleanup not staged: %+v %v", task, err)
			}
			var operation md.AccessOperation
			if err := db.Where("invite_id = ? AND kind = ?", 1, "revoke").First(&operation).Error; err != nil || operation.CleanupCycleID == 0 {
				t.Fatalf("missing retirement: %+v %v", operation, err)
			}
			if scenario == "failed" || scenario == "unknown" {
				if err := db.Model(&operation).Update("state", scenario).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct the backend to simulate a crash after the local checkpoint.
			executor.Backend = New(db)
			replayed, err := executor.Resume(context.Background(), task.ID, task.Version, 3)
			if err != nil || replayed.State != d.Fencing {
				t.Fatalf("unsafe resume: %+v %v", replayed, err)
			}
			var count int64
			if err := db.Model(&md.IdentityMigration{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("premature mapping: %d %v", count, err)
			}
			if err := db.Model(&md.AccessOperation{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("replayed revoke: %d %v", count, err)
			}
			if scenario == "failed" || scenario == "unknown" {
				return
			}
			// Only an explicit persisted runtime success releases the commit guard.
			if err := db.Model(&operation).Update("state", "succeeded").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&invite).Update("state", "revoked").Error; err != nil {
				t.Fatal(err)
			}
			finished, err := executor.Resume(context.Background(), task.ID, task.Version, 3)
			if err != nil || finished.State != d.Recovering {
				t.Fatalf("commit blocked after revoke: %+v %v", finished, err)
			}
			var mapping md.IdentityMigration
			if err := db.First(&mapping).Error; err != nil || mapping.State != "awaiting_member" || mapping.AdmittedAt != nil {
				t.Fatalf("member ordering lost: %+v %v", mapping, err)
			}
		})
	}
}

// confirmedCancellation creates a synthetic confirmed task and returns its exact cancellation CAS.
func confirmedCancellation(t *testing.T, executor *a.IdentityExecutor, input a.IdentityPreflightInput) c.Cancellation {
	t.Helper()
	task, err := executor.Preview(context.Background(), 3, input)
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Confirm(context.Background(), task.ID, c.Confirmation{ActorID: 3, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	return c.Cancellation{TaskID: task.ID, ActorID: 3, ExpectedVersion: task.Version}
}

// TestIdentityCancelAfterDrift proves failed commit can be cancelled and a fresh review reacquires scope.
func TestIdentityCancelAfterDrift(t *testing.T) {
	db, executor, input := fixture(t)
	ctx := context.Background()
	cancellation := confirmedCancellation(t, executor, input)
	if err := db.Exec("UPDATE telegram_group_targets SET route_version=12").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Resume(ctx, cancellation.TaskID, cancellation.ExpectedVersion, 3); !errors.Is(err, m.ErrConflict) {
		t.Fatal("drift accepted", err)
	}
	task, err := executor.Cancel(ctx, cancellation)
	if err != nil || task.State != d.Cancelled {
		t.Fatal("cancel failed", err)
	}
	replay, err := executor.Cancel(ctx, cancellation)
	if err != nil || replay.Version != task.Version {
		t.Fatal("replay changed version", err)
	}
	input.RequestKey = "fresh-review-after-cancel"
	next := confirmedCancellation(t, executor, input)
	if next.TaskID == cancellation.TaskID {
		t.Fatal("reused cancelled parent")
	}
	// A replay must never release slots acquired by the replacement task.
	if _, err := executor.Cancel(ctx, cancellation); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", next.TaskID).Count(&count).Error; err != nil || count == 0 {
		t.Fatal("replacement scope released", err)
	}
}

// TestIdentityCancelRejectsEvidence checks every step state and both endpoint positions in overlapping mappings.
func TestIdentityCancelRejectsEvidence(t *testing.T) {
	for _, state := range []d.StepState{d.StepPending, d.StepRunning, d.StepSucceeded, d.StepFailed, d.StepUnknown} {
		t.Run(string(state), func(t *testing.T) {
			db, executor, input := fixture(t)
			cancellation := confirmedCancellation(t, executor, input)
			if err := db.Create(&d.Step{TaskID: cancellation.TaskID, BusinessKey: "synthetic", Operation: "synthetic", State: state, Version: 1}).Error; err != nil {
				t.Fatal(err)
			}
			assertCancelRefused(t, db, executor, cancellation)
		})
	}
	for _, identity := range []int64{9007199254740993, 9007199254740995} {
		for _, oldEndpoint := range []bool{false, true} {
			db, executor, input := fixture(t)
			cancellation := confirmedCancellation(t, executor, input)
			row := md.IdentityMigration{AccountID: 999, TargetID: 1, OldTelegramID: 12, NewTelegramID: 13, State: "completed"}
			if oldEndpoint {
				row.OldTelegramID = identity
			} else {
				row.NewTelegramID = identity
			}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			assertCancelRefused(t, db, executor, cancellation)
		}
	}
}

// assertCancelRefused requires a conflict without changing the confirmed parent's state or scope.
func assertCancelRefused(t *testing.T, db *gorm.DB, executor *a.IdentityExecutor, input c.Cancellation) {
	t.Helper()
	if _, err := executor.Cancel(context.Background(), input); err == nil {
		t.Fatal("unsafe cancellation accepted")
	}
	detail, err := executor.Store.Get(context.Background(), input.TaskID)
	if err != nil || detail.Task.State != d.Fencing || detail.Task.Version != input.ExpectedVersion {
		t.Fatal("failed cancellation mutated parent", err)
	}
	var count int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", input.TaskID).Count(&count).Error; err != nil || count == 0 {
		t.Fatal("failed cancellation released scope", err)
	}
}

// TestIdentityCancelRollback proves a scope-delete failure rolls back cancellation CAS and supports retry.
func TestIdentityCancelRollback(t *testing.T) {
	db, executor, input := fixture(t)
	cancellation := confirmedCancellation(t, executor, input)
	if err := db.Exec("CREATE TRIGGER fail_identity_cancel BEFORE DELETE ON telegram_recovery_scope_slots BEGIN SELECT RAISE(ABORT, 'synthetic cancellation failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	assertCancelRefused(t, db, executor, cancellation)
	if err := db.Exec("DROP TRIGGER fail_identity_cancel").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Cancel(context.Background(), cancellation); err != nil {
		t.Fatal(err)
	}
}

// TestIdentityCancelAuthorizationAndPreflight checks original actor, current action grant and existing preflight cancellation.
func TestIdentityCancelAuthorizationAndPreflight(t *testing.T) {
	_, executor, input := fixture(t)
	ctx := context.Background()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	cancellation := c.Cancellation{TaskID: task.ID, ActorID: 3, ExpectedVersion: task.Version}
	executor.Authorize = func(_ context.Context, actor uint, action string) error {
		if action != "identity-cancel" {
			t.Fatal("wrong grant", action)
		}
		return c.ErrConflict
	}
	if _, err := executor.Cancel(ctx, cancellation); !errors.Is(err, c.ErrConflict) {
		t.Fatal("grant bypassed", err)
	}
	executor.Authorize = func(context.Context, uint, string) error { return nil }
	wrong := cancellation
	wrong.ActorID = 4
	if _, err := executor.Cancel(ctx, wrong); !errors.Is(err, c.ErrStale) {
		t.Fatal("foreign actor accepted", err)
	}
	task, err = executor.Cancel(ctx, cancellation)
	if err != nil || task.State != d.Cancelled || task.ConfirmedAt != nil {
		t.Fatal("preflight cancellation broken", err)
	}
	if _, err := executor.Cancel(ctx, cancellation); err != nil {
		t.Fatal("preflight replay broken", err)
	}
}

// TestIdentityCancelCommitOrdering exercises both parent-lock serializations using a stale preloaded detail.
func TestIdentityCancelCommitOrdering(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		db, executor, input := fixture(t)
		cancellation := confirmedCancellation(t, executor, input)
		ctx := context.Background()
		detail, err := executor.Store.Get(ctx, cancellation.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if cancelFirst {
			if _, err := executor.Cancel(ctx, cancellation); err != nil {
				t.Fatal(err)
			}
			if err := executor.Backend.Commit(ctx, detail); !errors.Is(err, c.ErrStale) {
				t.Fatal("commit resurrected cancelled parent", err)
			}
			var count int64
			if err := db.Model(&md.IdentityMigration{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("cancel-first wrote mappings", err)
			}
		} else {
			if err := executor.Backend.Commit(ctx, detail); err != nil {
				t.Fatal(err)
			}
			if _, err := executor.Cancel(ctx, cancellation); !errors.Is(err, c.ErrStale) {
				t.Fatal("cancel discarded committed mapping", err)
			}
		}
	}
}

// TestIdentityCancelConcurrentCommit gates the first parent's locked read while the competing
// operation queues on the single connection; each winner must exclude the other atomically.
func TestIdentityCancelConcurrentCommit(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		db, executor, input := fixture(t)
		cancellation := confirmedCancellation(t, executor, input)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		detail, err := executor.Store.Get(ctx, cancellation.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		if err := db.Callback().Query().After("gorm:query").Register("identity_cancel_lock_gate", func(query *gorm.DB) {
			if query.Statement.Table == "telegram_recovery_tasks" {
				once.Do(func() {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
			}
		}); err != nil {
			t.Fatal(err)
		}
		commit := func() error { return executor.Backend.Commit(ctx, detail) }
		abort := func() error { _, err := executor.Backend.CancelConfirmed(ctx, cancellation); return err }
		first, second := commit, abort
		if cancelFirst {
			first, second = abort, commit
		}
		firstResult, secondResult := make(chan error, 1), make(chan error, 1)
		go func() { firstResult <- first() }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		go func() { secondResult <- second() }()
		close(release)
		if err := <-firstResult; err != nil {
			t.Fatal("winning transaction", err)
		}
		if err := <-secondResult; !errors.Is(err, c.ErrStale) {
			t.Fatal("loser was not fenced", err)
		}
		if err := db.Callback().Query().Remove("identity_cancel_lock_gate"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestIdentityCancelRejectsInvalidReview checks malformed evidence, exact versions and confirmation receipts.
func TestIdentityCancelRejectsInvalidReview(t *testing.T) {
	for _, patch := range []map[string]any{
		{"confirmed_actor_id": 4}, {"confirmed_version": 0}, {"snapshot_json": `{}`},
		{"snapshot_json": `{"identity_targets":[{"target_id":"invalid"}]}`},
	} {
		db, executor, input := fixture(t)
		cancellation := confirmedCancellation(t, executor, input)
		if err := db.Model(&d.Task{}).Where("id = ?", cancellation.TaskID).Updates(patch).Error; err != nil {
			t.Fatal(err)
		}
		assertCancelRefused(t, db, executor, cancellation)
	}
	db, executor, input := fixture(t)
	cancellation := confirmedCancellation(t, executor, input)
	wrong := cancellation
	wrong.ExpectedVersion++
	if _, err := executor.Cancel(context.Background(), wrong); !errors.Is(err, c.ErrStale) {
		t.Fatal("wrong version accepted", err)
	}
	// Narrow identity cancellation must not broaden either generic transition path.
	if _, err := executor.Store.Cancel(context.Background(), cancellation); !errors.Is(err, c.ErrStale) {
		t.Fatal("generic cancel widened", err)
	}
	if _, err := executor.Store.Transition(context.Background(), c.Transition{TaskID: cancellation.TaskID, ExpectedVersion: cancellation.ExpectedVersion, State: d.Cancelled}); !errors.Is(err, c.ErrStale) {
		t.Fatal("generic transition widened", err)
	}
	if _, err := executor.Cancel(context.Background(), cancellation); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", cancellation.TaskID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("scope retained", err)
	}
}

// fixture wires real stores on MaxOpenConns=1; it never connects to Telegram or production data.
func fixture(t *testing.T) (*gorm.DB, *a.IdentityExecutor, a.IdentityPreflightInput) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "identity.db")), &gorm.Config{})
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
	models := append(tasks.Models(), &md.LedgerSource{}, &md.LedgerRevocation{}, &md.LedgerProjection{}, &md.AccessMember{}, &md.IdentityMigration{}, &md.AccessInvite{}, &md.AccessOperation{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, deleted_at DATETIME)",
		"CREATE TABLE user_oauth_identities (id INTEGER PRIMARY KEY, user_id INTEGER, provider TEXT, provider_user_id TEXT)",
		"CREATE TABLE telegram_group_targets (id INTEGER PRIMARY KEY, purpose TEXT, enabled BOOLEAN, recovery_task_id INTEGER, version INTEGER, route_version INTEGER, bot_instance_id INTEGER, telegram_chat_id INTEGER)",
		"INSERT INTO users VALUES (7, 'active', NULL)",
		"INSERT INTO user_oauth_identities VALUES (1,7,'telegram','9007199254740995')",
		"INSERT INTO telegram_group_targets VALUES (1,'membership',true,0,9,11,5,-10001)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []any{&md.LedgerSource{ID: 9, OrderItemID: 8, TargetID: 1, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, PaidAt: time.Now().UTC().Add(-time.Hour), Kind: md.TermMonths, Months: 1, SnapshotHash: "synthetic"}, &md.LedgerProjection{BeneficiaryTelegramID: 9007199254740993, TargetID: 1, Version: 9007199254740993}} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	executor := &a.IdentityExecutor{Store: tasks.New(db), Backend: New(db), Authorize: func(_ context.Context, actor uint, _ string) error {
		if actor != 3 {
			return c.ErrInvalid
		}
		return nil
	}}
	input := a.IdentityPreflightInput{IdentityMigrationInput: m.IdentityMigrationInput{AccountID: 7, OldTelegramID: "9007199254740993", NewTelegramID: "9007199254740995", TargetIDs: []string{"1"}}, RequestKey: "identity-test", Reason: "lost identity", VerificationNote: "administrator verified account ownership", HumanVerified: true}
	return db, executor, input
}

// TestIdentityRecoverySingleConnection covers idempotency, actor binding, atomic commit,
// runtime admission, cleanup proof, scope release and unchanged original paid provenance.
func TestIdentityRecoverySingleConnection(t *testing.T) {
	db, executor, input := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := executor.Preview(ctx, 3, input)
	if err != nil || replay.ID != task.ID || !replay.ExpiresAt.Equal(task.ExpiresAt) {
		t.Fatal("preflight replay", err)
	}
	confirmation := c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: task.Version, ActorID: 3}
	wrong := confirmation
	wrong.ActorID = 4
	if _, err := executor.Confirm(ctx, task.ID, wrong); err == nil {
		t.Fatal("foreign actor accepted")
	}
	var before md.LedgerSource
	if err := db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	task, err = executor.Confirm(ctx, task.ID, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Confirm(ctx, task.ID, confirmation); err != nil {
		t.Fatal("confirmation replay", err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Recovering {
		t.Fatalf("commit %+v %v", task, err)
	}
	detail, err := executor.Store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Backend.Commit(ctx, detail); err != nil {
		t.Fatal("commit replay", err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Recovering {
		t.Fatal("fabricated completion", err)
	}
	var row md.IdentityMigration
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	observation := m.AccessObservation{TargetID: 1, BotID: 5, ChatID: -10001, RouteVersion: 11, TelegramID: 9007199254740995, Status: "member", IsMember: true, ObservedAt: row.CreatedAt.Add(time.Second), Source: "query"}
	if err := mappings.Advance(db, observation); err != nil {
		t.Fatal(err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Recovering {
		t.Fatal("admission is not cleanup", err)
	}
	observation.TelegramID = 9007199254740993
	observation.Status = "left"
	observation.IsMember = false
	observation.ObservedAt = observation.ObservedAt.Add(time.Second)
	if err := mappings.Advance(db, observation); err != nil {
		t.Fatal(err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Completed {
		t.Fatalf("completion %+v %v", task, err)
	}
	var after md.LedgerSource
	if err := db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("historical source mutated")
	}
	var count int64
	if err := db.Model(&d.ScopeSlot{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("scope not released", err)
	}
	page, err := executor.Store.ListSteps(ctx, task.ID, 0, 20)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("missing receipts", err)
	}
}

// TestIdentityRecoveryRollback verifies stale post-confirm evidence rolls back every step and mapping.
func TestIdentityRecoveryRollback(t *testing.T) {
	db, executor, input := fixture(t)
	ctx := context.Background()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Confirm(ctx, task.ID, c.Confirmation{ActorID: 3, ExpectedVersion: task.Version, PreflightRef: task.PreflightRef})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE telegram_group_targets SET route_version=12").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Resume(ctx, task.ID, task.Version, 3); !errors.Is(err, m.ErrConflict) {
		t.Fatal("stale route accepted", err)
	}
	detail, err := executor.Store.Get(ctx, task.ID)
	if err != nil || detail.Task.State != d.Fencing {
		t.Fatal("partial state", err)
	}
	for _, model := range []any{&md.IdentityMigration{}, &d.Step{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("partial commit", err)
		}
	}
}

// TestIdentityRecoveryTTLAndReview denies expired previews and absent human attestations.
func TestIdentityRecoveryTTLAndReview(t *testing.T) {
	db, executor, input := fixture(t)
	ctx := context.Background()
	input.HumanVerified = false
	if _, err := executor.Preview(ctx, 3, input); !errors.Is(err, c.ErrInvalid) {
		t.Fatal(err)
	}
	input.HumanVerified = true
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&d.Task{}).Where("id = ?", task.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Confirm(ctx, task.ID, c.Confirmation{ActorID: 3, ExpectedVersion: task.Version, PreflightRef: task.PreflightRef}); !errors.Is(err, c.ErrExpired) {
		t.Fatal("expired confirmed", err)
	}
}

// TestIdentityReceiptFailureRollsBackMappings simulates a crash boundary after the writer
// but before the recovery receipt, proving the single transaction can be retried safely.
func TestIdentityReceiptFailureRollsBackMappings(t *testing.T) {
	db, executor, input := fixture(t)
	ctx := context.Background()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Confirm(ctx, task.ID, c.Confirmation{ActorID: 3, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_identity_receipt BEFORE INSERT ON telegram_recovery_steps BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Resume(ctx, task.ID, task.Version, 3); err == nil {
		t.Fatal("receipt failure ignored")
	}
	var count int64
	if err := db.Model(&md.IdentityMigration{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("orphaned mapping", count, err)
	}
	detail, err := executor.Store.Get(ctx, task.ID)
	if err != nil || detail.Task.State != d.Fencing || detail.Task.Version != task.Version {
		t.Fatal("partial parent checkpoint", err)
	}
	if err := db.Exec("DROP TRIGGER fail_identity_receipt").Error; err != nil {
		t.Fatal(err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Recovering {
		t.Fatal("safe retry failed", err)
	}
}

// TestIdentityScopeExclusion blocks a second reviewed task for either protected identity.
func TestIdentityScopeExclusion(t *testing.T) {
	_, executor, input := fixture(t)
	ctx := context.Background()
	first, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestKey = "second-review"
	second, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Confirm(ctx, first.ID, c.Confirmation{ActorID: 3, PreflightRef: first.PreflightRef, ExpectedVersion: first.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Confirm(ctx, second.ID, c.Confirmation{ActorID: 3, PreflightRef: second.PreflightRef, ExpectedVersion: second.Version}); err != c.ErrScopeBusy {
		t.Fatal("overlapping migration accepted", err)
	}
}

// TestIdentityCompletionCrashRollsBackRelease simulates failure after real member
// observations but before the parent completion write. Receipt and scope release
// must roll back together; a reconstructed executor may safely resume exactly once.
func TestIdentityCompletionCrashRollsBackRelease(t *testing.T) {
	db, executor, input := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := executor.Preview(ctx, 3, input)
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Confirm(ctx, task.ID, c.Confirmation{ActorID: 3, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, err = executor.Resume(ctx, task.ID, task.Version, 3)
	if err != nil {
		t.Fatal(err)
	}
	var original md.LedgerSource
	var mapping md.IdentityMigration
	if err := db.First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	observation := m.AccessObservation{TargetID: 1, BotID: 5, ChatID: -10001, RouteVersion: 11, TelegramID: mapping.NewTelegramID, Status: "member", IsMember: true, ObservedAt: mapping.CreatedAt.Add(time.Second), Source: "query"}
	if err := mappings.Advance(db, observation); err != nil {
		t.Fatal(err)
	}
	observation.TelegramID, observation.Status, observation.IsMember = mapping.OldTelegramID, "left", false
	observation.ObservedAt = observation.ObservedAt.Add(time.Second)
	if err := mappings.Advance(db, observation); err != nil {
		t.Fatal(err)
	}
	var scopesBefore int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", task.ID).Count(&scopesBefore).Error; err != nil || scopesBefore == 0 {
		t.Fatal("missing protected scope", err)
	}
	if err := db.Exec("CREATE TRIGGER fail_identity_completion BEFORE UPDATE OF state ON telegram_recovery_tasks WHEN NEW.state = 'completed' BEGIN SELECT RAISE(ABORT, 'synthetic completion interruption'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Resume(ctx, task.ID, task.Version, 3); err == nil {
		t.Fatal("completion interruption ignored")
	}
	var scopesAfter, receipts int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", task.ID).Count(&scopesAfter).Error; err != nil || scopesAfter != scopesBefore {
		t.Fatal("interrupted completion released scope", err)
	}
	if err := db.Model(&d.Step{}).Where("task_id = ? AND business_key = ?", task.ID, "identity_cleanup_verified").Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatal("interrupted completion committed receipt", err)
	}
	detail, err := executor.Store.Get(ctx, task.ID)
	if err != nil || detail.Task.State != d.Recovering || detail.Task.Version != task.Version {
		t.Fatal("interrupted completion changed parent", err)
	}
	if err := db.Exec("DROP TRIGGER fail_identity_completion").Error; err != nil {
		t.Fatal(err)
	}
	restarted := &a.IdentityExecutor{Store: tasks.New(db), Backend: New(db), Authorize: executor.Authorize}
	if _, err := restarted.Resume(ctx, task.ID, task.Version, 4); err == nil {
		t.Fatal("restart bypassed current administrator authorization")
	}
	task, err = restarted.Resume(ctx, task.ID, task.Version, 3)
	if err != nil || task.State != d.Completed {
		t.Fatal("restart could not finish verified cleanup", err)
	}
	if _, err := restarted.Resume(ctx, task.ID, task.Version, 3); err != nil {
		t.Fatal("completion replay failed", err)
	}
	if err := db.Model(&d.Step{}).Where("task_id = ? AND business_key = ?", task.ID, "identity_cleanup_verified").Count(&receipts).Error; err != nil || receipts != 1 {
		t.Fatal("completion receipt not exactly once", err)
	}
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", task.ID).Count(&scopesAfter).Error; err != nil || scopesAfter != 0 {
		t.Fatal("verified completion retained scope", err)
	}
	if _, err := restarted.Resume(ctx, task.ID, detail.Task.Version, 3); !errors.Is(err, c.ErrStale) {
		t.Fatal("restart accepted stale parent version", err)
	}
	var unchanged md.LedgerSource
	if err := db.First(&unchanged).Error; err != nil || !reflect.DeepEqual(original, unchanged) {
		t.Fatal("restart changed historical provenance", err)
	}
}
