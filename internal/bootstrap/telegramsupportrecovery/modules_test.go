package telegramsupportrecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	admin "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	br "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/support/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	sd "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testCipher confines fake credentials to the temporary database.
type testCipher struct{}

func (testCipher) Open(value string) (string, error) { return value, nil }
func (testCipher) Seal(value string) (string, error) { return value, nil }

// testForum obtains the sole connection at the external boundary, detecting HTTP-in-tx deadlocks.
type testForum struct {
	db       *gorm.DB
	calls    int
	onCreate func(context.Context) error
}

func (f *testForum) CreateForumTopic(ctx context.Context, _ string, in r.ForumTopicRequest) (r.ForumTopicResult, error) {
	var count int64
	if err := f.db.WithContext(ctx).Model(&sd.Session{}).Count(&count).Error; err != nil {
		return r.ForumTopicResult{}, err
	}
	f.calls++
	if f.onCreate != nil {
		if err := f.onCreate(ctx); err != nil {
			return r.ForumTopicResult{}, err
		}
	}
	return r.ForumTopicResult{ThreadID: 222}, nil
}
func (*testForum) CloseForumTopic(context.Context, string, r.ForumTopicRequest) error {
	return c.ErrDenied
}
func (*testForum) ReopenForumTopic(context.Context, string, r.ForumTopicRequest) error {
	return c.ErrDenied
}

// TestDurableReceiptCheckpoint recovers exact successful/failed checkpoints
// after completion loss. Alive claims, wrong task/CAS and unbound legacy stay blocked.
func TestDurableReceiptCheckpoint(t *testing.T) {
	for _, group := range []bool{true, false} {
		for _, succeeded := range []bool{true, false} {
			t.Run(fmt.Sprintf("group=%t/succeeded=%t", group, succeeded), func(t *testing.T) {
				a, groupRequest, _ := groupFixture(t)
				var botRequest br.ModuleRequest
				if !group {
					a, botRequest, _ = moduleFixture(t, 1)
				}
				ctx := context.Background()
				run := func() error {
					if group {
						_, err := a.RecoverGroup(ctx, 5, groupRequest, joinedDrainer{a.DB})
						return err
					}
					_, err := a.Run(ctx, botRequest)
					return err
				}
				if err := a.DB.Exec("CREATE TRIGGER lose_checkpoint BEFORE UPDATE ON telegram_recovery_steps WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'lost'); END").Error; err != nil {
					t.Fatal(err)
				}
				if err := run(); err == nil {
					t.Fatal("expected lost completion")
				}
				if err := a.DB.Exec("DROP TRIGGER lose_checkpoint").Error; err != nil {
					t.Fatal(err)
				}
				entry, in := managementResolutionFixture(t, ctx, a)
				in.ThreadID = 222
				if !succeeded {
					// Equivalent durable failed result, without a mapping; version/claim retained.
					if err := a.DB.Where("operation_id = ?", in.OperationID).Delete(&sd.TopicMapping{}).Error; err != nil {
						t.Fatal(err)
					}
					if err := a.DB.Model(&sd.TopicOperation{}).Where("id = ?", in.OperationID).Update("state", "failed").Error; err != nil {
						t.Fatal(err)
					}
					if err := a.DB.Model(&sd.RestorationEntry{}).Where("id = ?", entry.ID).Update("state", "failed").Error; err != nil {
						t.Fatal(err)
					}
					in.Decision, in.ThreadID = "failed", 0
				}
				if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
					t.Fatal("alive claim accepted")
				}
				var step rd.Step
				if err := a.DB.Where("task_id = ? AND state = ?", 9, rd.StepRunning).Take(&step).Error; err != nil {
					t.Fatal(err)
				}
				if err := a.DB.Model(&step).Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
				if succeeded {
					if _, err := tasks.New(a.DB).ClaimStep(ctx, rc.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute}); err == nil {
						t.Fatal("expired step replayed")
					}
					entry, in = managementResolutionFixture(t, ctx, a)
					in.ThreadID = 222
				}
				stale := in
				stale.OperationVersion++
				if err := a.ResolveSupport(ctx, 5, 9, entry.ID, stale); err == nil {
					t.Fatal("stale operation accepted")
				}
				if err := a.ResolveSupport(ctx, 5, 99, entry.ID, in); err == nil {
					t.Fatal("cross task accepted")
				}
				for _, evidence := range []string{"", " ", "\t\n", "verified", "case:", "case:bad space", "https://example.test/proof"} {
					invalid := in
					invalid.Evidence = evidence
					if err := a.ResolveSupport(ctx, 5, 9, entry.ID, invalid); err == nil {
						t.Fatalf("invalid evidence accepted: %q", evidence)
					}
				}
				originalKey := step.BusinessKey
				if err := a.DB.Model(&step).Update("business_key", fmt.Sprintf("support.%d.%d", entry.TargetID, in.Revision)).Error; err != nil {
					t.Fatal(err)
				}
				if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
					t.Fatal("unbound historical step cleared")
				}
				if err := a.DB.Model(&step).Update("business_key", originalKey).Error; err != nil {
					t.Fatal(err)
				}
				if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != nil {
					t.Fatal(err)
				}
				if err := a.DB.First(&step, step.ID).Error; err != nil {
					t.Fatal(err)
				}
				if step.State != rd.StepSucceeded {
					t.Fatalf("checkpoint = %s", step.State)
				}
				if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
					t.Fatal("stale revision accepted")
				}
			})
		}
	}
}

// TestAtomicSupportClaimRollback injects interruption at each pre-HTTP boundary.
// Both same/cross bot paths must roll back all claimed state without any API call.
func TestAtomicSupportClaimRollback(t *testing.T) {
	for _, group := range []bool{true, false} {
		for _, boundary := range []string{"claim", "binding"} {
			t.Run(fmt.Sprintf("%t/%s", group, boundary), func(t *testing.T) {
				a, groupRequest, forum := groupFixture(t)
				var botRequest br.ModuleRequest
				if !group {
					a, botRequest, forum = moduleFixture(t, 1)
				}
				condition := "NEW.state = 'running'"
				if boundary == "binding" {
					condition = "NEW.business_key <> OLD.business_key"
				}
				if err := a.DB.Exec("CREATE TRIGGER interrupt_claim AFTER UPDATE ON telegram_recovery_steps WHEN " + condition + " BEGIN SELECT RAISE(ABORT, 'interrupted'); END").Error; err != nil {
					t.Fatal(err)
				}
				run := func() error {
					if group {
						_, err := a.RecoverGroup(context.Background(), 5, groupRequest, joinedDrainer{a.DB})
						return err
					}
					_, err := a.Run(context.Background(), botRequest)
					return err
				}
				if err := run(); err == nil {
					t.Fatal("interruption ignored")
				}
				var count int64
				if err := a.DB.Model(&rd.Step{}).Where("task_id = ? AND state = ?", 9, rd.StepRunning).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 || forum.calls != 0 {
					t.Fatal("partial claim or external effect")
				}
				if err := a.DB.Exec("DROP TRIGGER interrupt_claim").Error; err != nil {
					t.Fatal(err)
				}
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if forum.calls != 1 {
					t.Fatal("resume did not execute exactly once")
				}
			})
		}
	}
}

// TestAuditedCheckpointContinuation simulates loss of the parent completion
// after a durable unknown. Both production module branches must retain fences
// until exact management CAS/audit, and must never replay the uncertain create.
func TestAuditedCheckpointContinuation(t *testing.T) {
	for _, group := range []bool{true, false} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			var a *Adapter
			var forum *testForum
			var groupRequest rc.GroupRecoveryRequest
			var botRequest br.ModuleRequest
			if group {
				a, groupRequest, forum = groupFixture(t)
			} else {
				a, botRequest, forum = moduleFixture(t, 1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			run := func() error {
				if group {
					_, err := a.ContinueGroup(ctx, 5, groupRequest, joinedDrainer{a.DB})
					return err
				}
				_, err := a.ContinueBot(ctx, botRequest)
				return err
			}
			// A DB trigger drops only the parent completion, not business persistence.
			if err := a.DB.Exec("CREATE TRIGGER lose_support_completion BEFORE UPDATE ON telegram_recovery_steps WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'completion lost'); END").Error; err != nil {
				t.Fatal(err)
			}
			forum.onCreate = func(context.Context) error { return c.ErrDenied }
			if err := run(); err == nil {
				t.Fatal("completion unexpectedly persisted")
			}
			if err := a.DB.Exec("DROP TRIGGER lose_support_completion").Error; err != nil {
				t.Fatal(err)
			}
			var step rd.Step
			if err := a.DB.Where("task_id = ? AND state = ?", 9, rd.StepRunning).Take(&step).Error; err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-time.Minute)
			if err := a.DB.Model(&step).Update("lease_until", past).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := tasks.New(a.DB).ClaimStep(ctx, rc.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute}); err == nil {
				t.Fatal("expired attempt replayed")
			}
			if err := run(); err == nil {
				t.Fatal("unaudited parent resumed")
			}
			entry, in := managementResolutionFixture(t, ctx, a)
			var err error
			stale := in
			stale.Revision++
			if err = a.ResolveSupport(ctx, 5, 9, entry.ID, stale); err == nil {
				t.Fatal("stale evidence advanced")
			}
			permission := a.Permission
			a.Permission = func(context.Context, uint, string) error { return c.ErrDenied }
			if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
				t.Fatal("revoked permission advanced")
			}
			a.Permission = permission
			if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != nil {
				t.Fatal(err)
			}
			if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
				t.Fatal("late resolution accepted")
			}
			forum.onCreate = nil
			if err = run(); err != nil {
				t.Fatal(err)
			}
			if forum.calls != 1 {
				t.Fatalf("unknown replayed: calls %d", forum.calls)
			}
			var parent rd.Task
			if err = a.DB.First(&parent, 9).Error; err != nil {
				t.Fatal(err)
			}
			if group && parent.State != rd.Completed {
				t.Fatalf("group not released: %s", parent.State)
			}
			if !group && parent.State != rd.Recovering {
				t.Fatal("support proof completed entire bot parent")
			}
		})
	}
}

// managementResolutionFixture reads server CAS projections for the fixture's
// first entry; callers still submit through the authenticated management port.
func managementResolutionFixture(t *testing.T, ctx context.Context, a *Adapter) (rc.SupportEntry, rc.SupportResolution) {
	t.Helper()
	page, err := a.SupportEntries(ctx, 5, 9, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	entry := page.Items[0]
	ops, err := a.SupportOperations(ctx, 5, 9, entry.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range ops.Items {
		if operation.ID == entry.OperationID {
			return entry, rc.SupportResolution{TaskVersion: page.TaskVersion, Revision: entry.Revision, OperationID: operation.ID, OperationVersion: operation.Version, Decision: "succeeded", ThreadID: 333, Evidence: "case:verified"}
		}
	}
	t.Fatal("missing operation")
	return entry, rc.SupportResolution{}
}

// TestResolutionDoesNotClearOtherSteps covers generic failure/unknown isolation
// and revoked scope evidence using the real single-connection management path.
func TestResolutionDoesNotClearOtherSteps(t *testing.T) {
	for _, state := range []rd.StepState{rd.StepFailed, rd.StepUnknown} {
		t.Run(string(state), func(t *testing.T) {
			a, request, forum := groupFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			forum.onCreate = func(context.Context) error { return c.ErrDenied }
			if _, err := a.RecoverGroup(ctx, 5, request, joinedDrainer{a.DB}); err != nil {
				t.Fatal(err)
			}
			other := rd.Step{TaskID: 9, BusinessKey: "other.module", Operation: "recover_module", State: state, Version: 1}
			if err := a.DB.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if err := a.DB.Model(&rd.Task{}).Where("id = ?", 9).Update("state", rd.NeedsReview).Error; err != nil {
				t.Fatal(err)
			}
			entry, in := managementResolutionFixture(t, ctx, a)
			if err := a.DB.Model(&gd.Target{}).Where("id = ?", request.TargetID).Update("recovery_task_id", 0).Error; err != nil {
				t.Fatal(err)
			}
			if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
				t.Fatal("revoked fence advanced")
			}
			if err := a.DB.Model(&gd.Target{}).Where("id = ?", request.TargetID).Update("recovery_task_id", 9).Error; err != nil {
				t.Fatal(err)
			}
			if err := a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != nil {
				t.Fatal(err)
			}
			var persisted rd.Step
			if err := a.DB.First(&persisted, other.ID).Error; err != nil || persisted.State != state {
				t.Fatal("other step changed", err)
			}
			if _, err := a.ContinueGroup(ctx, 5, request, joinedDrainer{a.DB}); err == nil {
				t.Fatal("other unknown/failed ignored")
			}
			if forum.calls != 1 {
				t.Fatal("unknown replayed")
			}
		})
	}
}

// TestDisabledDestinationTaskTopicUsesRealDrain keeps the low-level worker regression.
func TestDisabledDestinationTaskTopicUsesRealDrain(t *testing.T) {
	a, request, forum := moduleFixture(t, 1)
	detailVersion := uint64(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, permitKey{}, permit{Request: request, Version: detailVersion})
	plan := supportPlan(request, request.Snapshot.BotRoutes[0])
	store := a.Store()
	progress, err := store.BeginSupportRestoration(ctx, plan, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AdvanceSupportRestoration(ctx, 9, progress.Revision, 100, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// Production API refuses the ordinary worker without a persisted step lease.
	worker := app.RestorationWorker{Store: store, Platform: a, NewClaim: func() string { return "no-step" }, Now: time.Now}
	if err = worker.RunTopic(ctx, 9); err == nil {
		t.Fatal("missing execution step accepted")
	}
	if forum.calls != 0 {
		t.Fatal("unleased API called")
	}
}

// moduleFixture builds the real parent/scope/route/admin model on one connection.
func moduleFixture(t *testing.T, targetCount int) (*Adapter, br.ModuleRequest, *testForum) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "module.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(db.AutoMigrate(&admin.Admin{}, &gd.Target{}, &gd.Route{}, &gd.ModuleConfig{}, &rd.Task{}, &rd.ScopeSlot{}, &rd.Step{}, &runtime.Instance{}, &runtime.GenerationExit{}, &runtime.Delivery{}, &sd.RestorationDownstreamLiability{}, &sd.RestorationCheckpoint{}, &sd.RestorationEntry{}, &sd.Session{}, &sd.Challenge{}, &sd.TopicOperation{}, &sd.TopicMapping{}, &sd.BufferedMessage{}, &sd.Downstream{}, &sd.SupportBlock{}, &sd.Album{}, &sd.AlbumItem{}, &sd.ReadyDispatch{}, &sd.ResolutionAudit{}, &sd.ReplyMapping{}))
	now := time.Now().UTC()
	check(db.Create(&admin.Admin{ID: 5, Username: "test", PasswordHash: "test", IsSuper: true}).Error)
	request := br.ModuleRequest{TaskID: 9, ActorID: 5, Snapshot: rd.Snapshot{Old: rd.Endpoint{BotID: 1, TelegramID: 100}, New: rd.Endpoint{BotID: 2, TelegramID: 200}, ExpectedOld: rd.Versions{Config: 1, Generation: 1}, ExpectedNew: rd.Versions{Config: 1}, Scope: rd.Scope{BotIDs: []string{"1", "2"}, TelegramIDs: []string{"100", "200"}}}}
	check(db.Create(&runtime.Instance{ID: 1, Mode: "internal", State: "stopped", TelegramID: 100, Version: 1, Generation: 1, RecoveryTaskID: 9, RecoveryVersion: 1, RecoveryGeneration: 1}).Error)
	check(db.Create(&runtime.Instance{ID: 2, Mode: "internal", State: "stopped", TelegramID: 200, Version: 1, RecoveryTaskID: 9, RecoveryVersion: 1, TokenCipher: "test-only"}).Error)
	check(db.Create(&runtime.GenerationExit{BotID: 1, Version: 1, Generation: 1, JoinedAt: &now}).Error)
	for index := 1; index <= targetCount; index++ {
		target := uint(index)
		chat := int64(-100 - index)
		request.Snapshot.BotRoutes = append(request.Snapshot.BotRoutes, rd.BotRoute{TargetID: target, ChatID: chat, Version: 1, RouteVersion: 1, Purpose: "support", Enabled: true})
		request.Snapshot.Scope.TargetIDs = append(request.Snapshot.Scope.TargetIDs, fmt.Sprint(target))
		request.Snapshot.Scope.ChatIDs = append(request.Snapshot.Scope.ChatIDs, fmt.Sprint(chat))
		check(db.Create(&gd.Target{ID: target, BotInstanceID: 2, TelegramChatID: chat, Purpose: "support", Enabled: true, Version: 3, RouteVersion: 2, RecoveryTaskID: 9, OccupancySlot: fmt.Sprint(chat), Observation: gd.Observation{Generation: 0, BotTelegramID: 200, RouteVersion: 2, State: "ready", ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, IsForum: true, CanManageTopics: true, ObservedAt: &now}}).Error)
		check(db.Create(&sd.Session{ID: target, BotID: 1, UserID: int64(index + 40), ChatID: int64(index + 40), State: "open"}).Error)
		check(db.Create(&sd.Session{ID: target + 100, BotID: 2, UserID: int64(index + 40), ChatID: int64(index + 40), State: "open"}).Error)
		check(db.Create(&sd.Challenge{BotID: 2, SessionID: target + 100, Ref: fmt.Sprint(index), State: "passed", ConsumedAt: &now}).Error)
		check(db.Create(&sd.TopicOperation{BotID: 1, SessionID: target, TargetID: target, ChatID: chat, RouteVersion: 1, State: "succeeded"}).Error)
	}
	raw, err := json.Marshal(request.Snapshot)
	check(err)
	check(db.Create(&rd.Task{ID: 9, Kind: rd.BotReplace, State: rd.Recovering, Version: 4, ConfirmedAt: &now, ConfirmedActorID: 5, SnapshotJSON: string(raw), RequestKey: "test", PreflightRef: "test"}).Error)
	for prefix, values := range map[string][]string{"bot": request.Snapshot.Scope.BotIDs, "telegram": request.Snapshot.Scope.TelegramIDs, "target": request.Snapshot.Scope.TargetIDs, "chat": request.Snapshot.Scope.ChatIDs} {
		for _, value := range values {
			check(db.Create(&rd.ScopeSlot{Key: prefix + ":" + value, TaskID: 9}).Error)
		}
	}
	forum := &testForum{db: db}
	a := New(db, testCipher{}, forum, func(ctx context.Context, actor uint, action string) error {
		// This callback itself reads the only connection; invoking it inside a
		// transaction would deadlock, so the deadline proves boundary placement.
		var count int64
		return db.WithContext(ctx).Model(&admin.Admin{}).Where("id = ?", actor).Count(&count).Error
	})
	return a, request, forum
}

// TestRealModuleAuthorityMultiTarget verifies composite task/target checkpoints,
// actual topic execution, one support-only receipt and replay idempotency.
func TestRealModuleAuthorityMultiTarget(t *testing.T) {
	a, request, forum := moduleFixture(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	proofs, err := a.ContinueBot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 1 || proofs[0].Module != "support" || !proofs[0].LocalComplete || proofs[0].Remaining != 0 || forum.calls != 2 {
		t.Fatalf("proof %+v calls %d", proofs, forum.calls)
	}
	if _, err = a.ContinueBot(ctx, request); err != nil {
		t.Fatal(err)
	}
	if forum.calls != 2 {
		t.Fatal("duplicate external calls")
	}
	var count int64
	if err = a.DB.Model(&sd.RestorationCheckpoint{}).Where("task_id = ?", 9).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("plans %d %v", count, err)
	}
	drift := request
	drift.Snapshot.New.TelegramID = 999
	if _, err = a.ContinueBot(ctx, drift); err == nil {
		t.Fatal("snapshot drift accepted")
	}
	if err = a.DB.Model(&admin.Admin{}).Where("id = ?", 5).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.ContinueBot(ctx, request); err == nil {
		t.Fatal("revoked actor accepted")
	}
}

// TestRenewalPanicCancelsAndNeverReplays injects panic only inside renew's
// permission check after HTTP starts. Even a late success stays unknown.
func TestRenewalPanicCancelsAndNeverReplays(t *testing.T) {
	a, request, forum := moduleFixture(t, 1)
	var armed atomic.Bool
	original := a.Permission
	a.Permission = func(ctx context.Context, actor uint, action string) error {
		if armed.CompareAndSwap(true, false) {
			panic("secret renewal payload")
		}
		return original(ctx, actor, action)
	}
	forum.onCreate = func(ctx context.Context) error {
		armed.Store(true)
		<-ctx.Done()
		return nil // Simulate an HTTP implementation returning a late success.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() { _, err := a.Run(ctx, request); completed <- err }()
	select {
	case err := <-completed:
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe renewal error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("renewal panic did not cancel and join HTTP")
	}
	var entry sd.RestorationEntry
	if err := a.DB.Where("task_id = ?", request.TaskID).Take(&entry).Error; err != nil {
		t.Fatal(err)
	}
	var operation sd.TopicOperation
	if err := a.DB.First(&operation, entry.OperationID).Error; err != nil {
		t.Fatal(err)
	}
	var step rd.Step
	if err := a.DB.Where("task_id = ?", request.TaskID).Take(&step).Error; err != nil {
		t.Fatal(err)
	}
	if entry.State != "unknown" || operation.State != "unknown" || step.State == rd.StepSucceeded {
		t.Fatalf("lease loss confirmed result: entry=%s operation=%s step=%s", entry.State, operation.State, step.State)
	}
	var mappings int64
	if err := a.DB.Model(&sd.TopicMapping{}).Where("operation_id = ?", operation.ID).Count(&mappings).Error; err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatal("late success created an authoritative mapping")
	}
	forum.onCreate = nil
	_, _ = a.Run(context.Background(), request)
	if err := a.DB.Model(&step).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	_, _ = a.Run(context.Background(), request)
	if forum.calls != 1 {
		t.Fatalf("uncertain external action replayed: %d", forum.calls)
	}
}

// TestLeaseRenewalAndConcurrentResume keeps the mock HTTP joined while a second
// resume runs, proving one topic create and a persisted lease extension.
func TestLeaseRenewalAndConcurrentResume(t *testing.T) {
	a, request, forum := moduleFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var checks atomic.Int64
	original := a.Permission
	a.Permission = func(ctx context.Context, actor uint, action string) error {
		checks.Add(1)
		return original(ctx, actor, action)
	}
	entered := make(chan time.Time, 1)
	release := make(chan struct{})
	forum.onCreate = func(ctx context.Context) error {
		var step rd.Step
		if err := a.DB.WithContext(ctx).Where("task_id = ? AND state = ?", 9, rd.StepRunning).Take(&step).Error; err != nil {
			return err
		}
		entered <- *step.LeaseUntil
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	completed := make(chan error, 1)
	go func() { _, err := a.Run(ctx, request); completed <- err }()
	var initial time.Time
	select {
	case initial = <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := a.Run(ctx, request); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	renewed := false
	for !renewed {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			var step rd.Step
			if err := a.DB.WithContext(ctx).Where("task_id = ? AND state = ?", 9, rd.StepRunning).Take(&step).Error; err != nil {
				t.Fatal(err)
			}
			renewed = step.LeaseUntil.After(initial)
		}
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if forum.calls != 1 || checks.Load() < 5 {
		t.Fatalf("calls=%d permission=%d", forum.calls, checks.Load())
	}
}

// TestPermissionRevokedAfterHTTP rejects the mapping even when Telegram reports success.
func TestPermissionRevokedAfterHTTP(t *testing.T) {
	a, request, forum := moduleFixture(t, 1)
	forum.onCreate = func(ctx context.Context) error {
		return a.DB.WithContext(ctx).Model(&admin.Admin{}).Where("id = ?", 5).Update("deleted_at", time.Now()).Error
	}
	if _, err := a.Run(context.Background(), request); err == nil {
		t.Fatal("revoked result accepted")
	}
	var count int64
	if err := a.DB.Model(&sd.TopicMapping{}).Where("bot_id = ?", 2).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("mapping count=%d %v", count, err)
	}
}

// TestUnverifiedDestinationRemainsPending never proactively recreates an unstarted customer.
func TestUnverifiedDestinationRemainsPending(t *testing.T) {
	a, request, forum := moduleFixture(t, 1)
	if err := a.DB.Where("bot_id = ?", 2).Delete(&sd.Challenge{}).Error; err != nil {
		t.Fatal(err)
	}
	proof, err := a.Run(context.Background(), request)
	if err != nil || proof.Remaining == 0 || proof.LocalComplete || forum.calls != 0 {
		t.Fatalf("proof=%+v calls=%d %v", proof, forum.calls, err)
	}
}

// joinedDrainer verifies fixture-generated genuine join rows; it cannot fabricate exits.
type joinedDrainer struct{ db *gorm.DB }

func (d joinedDrainer) BlockAndDrainRecovery(ctx context.Context, fence r.RecoveryFence) error {
	return bots.New(d.db).RecoveryDrained(ctx, fence)
}

// groupFixture converts the fixture to the real GroupReplace snapshot shape,
// whose Config belongs to the group rather than the runtime instance.
func groupFixture(t *testing.T) (*Adapter, rc.GroupRecoveryRequest, *testForum) {
	t.Helper()
	a, _, forum := moduleFixture(t, 1)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := rd.Snapshot{Old: rd.Endpoint{BotID: 1, TargetID: 1, ChatID: -101}, New: rd.Endpoint{BotID: 1, TargetID: 1, ChatID: -201}, ExpectedOld: rd.Versions{Config: 1, Route: 2}, ExpectedNew: rd.Versions{Generation: 1}, Scope: rd.Scope{BotIDs: []string{"1"}, TargetIDs: []string{"1"}, ChatIDs: []string{"-101", "-201"}}}
	raw, err := json.Marshal(snapshot)
	check(err)
	check(a.DB.Model(&rd.Task{}).Where("id = ?", 9).Updates(map[string]any{"kind": rd.GroupReplace, "snapshot_json": string(raw)}).Error)
	check(a.DB.Where("task_id = ?", 9).Delete(&rd.ScopeSlot{}).Error)
	for _, key := range []string{"bot:1", "target:1", "chat:-101", "chat:-201"} {
		check(a.DB.Create(&rd.ScopeSlot{TaskID: 9, Key: key}).Error)
	}
	check(a.DB.Model(&gd.Target{}).Where("id = ?", 1).Updates(map[string]any{"bot_instance_id": 1, "telegram_chat_id": -201, "route_version": 3}).Error)
	// GORM embedded observation column names are read from the model itself.
	var target gd.Target
	check(a.DB.First(&target, 1).Error)
	target.ObservationSeq = 1
	target.Observation.Sequence = 1
	target.Observation.Generation = 1
	target.Observation.BotTelegramID = 100
	target.Observation.RouteVersion = 3
	check(a.DB.Save(&target).Error)
	check(a.DB.AutoMigrate(&gd.RecoveryFence{}))
	check(a.DB.Create(&gd.RecoveryFence{TaskID: 9, TargetID: 1, OldBotID: 1, NewBotID: 1, OldChatID: -101, NewChatID: -201, InitialVersion: 1, InitialRouteVersion: 2, FrozenVersion: 2, ReplacedVersion: 3}).Error)
	check(a.DB.Create(&gd.Route{TargetID: 1, RouteVersion: 2, BotInstanceID: 1, TelegramChatID: -101}).Error)
	check(a.DB.Create(&gd.Route{TargetID: 1, RouteVersion: 1, BotInstanceID: 1, TelegramChatID: -301}).Error)
	check(a.DB.Model(&sd.TopicOperation{}).Where("bot_id = ?", 1).Updates(map[string]any{"route_version": 1, "chat_id": -301}).Error)
	return a, rc.GroupRecoveryRequest{TaskID: 9, ActorID: 5, TargetID: 1, BotID: 1, OldChatID: -101, NewChatID: -201, OldRouteVersion: 2, NewRouteVersion: 3, Generation: 1, Purpose: "support"}, forum
}

// TestGroupRestorationHistoryAndResume restores a non-current historical route
// to the new forum without importing private reachability or old platform IDs.
func TestGroupRestorationHistoryAndResume(t *testing.T) {
	a, request, forum := groupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drain := joinedDrainer{a.DB}
	result, err := a.RecoverGroup(ctx, 5, request, drain)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Remaining != 0 || forum.calls != 1 {
		t.Fatalf("%+v calls=%d", result, forum.calls)
	}
	if _, err = a.RecoverGroup(ctx, 5, request, drain); err != nil {
		t.Fatal(err)
	}
	if forum.calls != 1 {
		t.Fatal("duplicate topic")
	}
	var old sd.TopicOperation
	if err = a.DB.Where("bot_id = ? AND route_version = ?", 1, 1).Take(&old).Error; err != nil || !old.Retired {
		t.Fatalf("history %+v %v", old, err)
	}
	if err = a.DB.Model(&admin.Admin{}).Where("id = ?", 5).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverGroup(ctx, 5, request, drain); err == nil {
		t.Fatal("revoked actor accepted")
	}
}

// TestGroupContinuationFinalizesAtomically proves the support branch can finish
// without calling the running-runtime-only group release path or leaking scopes.
func TestGroupContinuationFinalizesAtomically(t *testing.T) {
	a, request, forum := groupFixture(t)
	ctx := context.Background()
	drain := joinedDrainer{a.DB}
	result, err := a.ContinueGroup(ctx, 5, request, drain)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Remaining != 0 || forum.calls != 1 {
		t.Fatalf("%+v", result)
	}
	var task rd.Task
	var bot runtime.Instance
	var target gd.Target
	for _, item := range []struct {
		model any
		id    uint
	}{{&task, 9}, {&bot, 1}, {&target, 1}} {
		if err = a.DB.First(item.model, item.id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if task.State != rd.Completed || bot.RecoveryTaskID != 0 || target.RecoveryTaskID != 0 {
		t.Fatalf("task=%s bot=%d target=%d", task.State, bot.RecoveryTaskID, target.RecoveryTaskID)
	}
	if _, err = a.ContinueGroup(ctx, 5, request, drain); err != nil {
		t.Fatal(err)
	}
	if forum.calls != 1 {
		t.Fatal("completed replay called API")
	}
}

// TestGroupHistoryIsolatesOtherTargets refuses session-wide message retirement
// while another active logical target owns that session's delivery context.
func TestGroupHistoryIsolatesOtherTargets(t *testing.T) {
	a, request, forum := groupFixture(t)
	unrelated := sd.TopicOperation{BotID: 1, SessionID: 1, TargetID: 77, ChatID: -777, RouteVersion: 1, State: "succeeded"}
	if err := a.DB.Create(&unrelated).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecoverGroup(context.Background(), 5, request, joinedDrainer{a.DB}); err == nil {
		t.Fatal("ambiguous session ownership accepted")
	}
	if err := a.DB.First(&unrelated, unrelated.ID).Error; err != nil || unrelated.Retired || forum.calls != 0 {
		t.Fatalf("unrelated retired=%v calls=%d %v", unrelated.Retired, forum.calls, err)
	}
}

// TestGroupHistoryRequiresExactRouteAndDrain denies a substituted old chat and
// missing exit proof before retiring any historical operation.
func TestGroupHistoryRequiresExactRouteAndDrain(t *testing.T) {
	for _, missingDrain := range []bool{false, true} {
		t.Run(fmt.Sprint(missingDrain), func(t *testing.T) {
			a, request, forum := groupFixture(t)
			var err error
			if missingDrain {
				err = a.DB.Model(&runtime.GenerationExit{}).Where("bot_id = ?", 1).Update("joined_at", nil).Error
			} else {
				err = a.DB.Model(&gd.Route{}).Where("target_id = ? AND route_version = ?", 1, 1).Update("telegram_chat_id", -999).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.RecoverGroup(context.Background(), 5, request, joinedDrainer{a.DB}); err == nil {
				t.Fatal("invalid history/drain accepted")
			}
			if forum.calls != 0 {
				t.Fatal("unauthorized platform call")
			}
		})
	}
}

// TestHistoricalManagementProjection exercises non-current route evidence
// through the production management DTO and strict audit/CAS path.
func TestHistoricalManagementProjection(t *testing.T) {
	a, request, forum := groupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.DB.Model(&sd.TopicOperation{}).Where("bot_id = ?", 1).Updates(map[string]any{"state": "unknown", "action": "create", "version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := a.RecoverGroup(ctx, 5, request, joinedDrainer{a.DB})
	if err != nil || result.Remaining != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	page, err := a.SupportEntries(ctx, 5, 9, 0, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	entry := page.Items[0]
	ops, err := a.SupportOperations(ctx, 5, 9, entry.ID, 0, 1)
	if err != nil || len(ops.Items) != 1 {
		t.Fatal(err)
	}
	old := ops.Items[0]
	if !old.Retired || old.RouteVersion != 1 || old.ChatID != -301 || old.TargetID != 1 || old.Action != "create" {
		t.Fatalf("lost history projection %+v", old)
	}
	in := rc.SupportResolution{TaskVersion: page.TaskVersion, Revision: entry.Revision, OperationID: old.ID, OperationVersion: old.Version, Decision: "retry", Evidence: "case:history"}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
		t.Fatal("legacy retry accepted")
	}
	in.Decision = "failed"
	in.Evidence = ""
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
		t.Fatal("missing evidence accepted")
	}
	in.Evidence = "case:history"
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
		t.Fatal("stale decrement")
	}
	result, err = a.ContinueGroup(ctx, 5, request, joinedDrainer{a.DB})
	if err != nil || result.Remaining != 0 || forum.calls != 1 {
		t.Fatalf("%+v %v calls=%d", result, err, forum.calls)
	}
	var operation sd.TopicOperation
	if err = a.DB.First(&operation, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operation.ChatID != -301 || operation.RouteVersion != 1 || !operation.Retired || operation.State != "failed" {
		t.Fatal("history changed")
	}
}

// TestGroupLegacyNoEffectAudit requires a versioned manual no-effect decision;
// unknown history is not automatically counted as complete or retried.
func TestGroupLegacyNoEffectAudit(t *testing.T) {
	a, request, _ := groupFixture(t)
	ctx := context.Background()
	drain := joinedDrainer{a.DB}
	if err := a.DB.Model(&sd.TopicOperation{}).Where("bot_id = ?", 1).Updates(map[string]any{"state": "unknown", "action": "create", "version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := a.RecoverGroup(ctx, 5, request, drain)
	if err != nil || result.Remaining != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	var entry sd.RestorationEntry
	var operation sd.TopicOperation
	if err = a.DB.Where("task_id = ?", 9).Take(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Where("bot_id = ? AND route_version = ?", 1, 1).Take(&operation).Error; err != nil {
		t.Fatal(err)
	}
	resolution := c.Resolution{ActorID: 5, ExpectedVersion: operation.Version, Decision: "failed", Evidence: "case:no-effect"}
	if err = a.ResolveGroupLegacy(ctx, 5, request, drain, entry.ID, operation.ID, resolution); err != nil {
		t.Fatal(err)
	}
	if err = a.ResolveGroupLegacy(ctx, 5, request, drain, entry.ID, operation.ID, resolution); err == nil {
		t.Fatal("duplicate legacy decrement")
	}
	result, err = a.RecoverGroup(ctx, 5, request, drain)
	if err != nil || result.Remaining != 0 {
		t.Fatalf("%+v %v", result, err)
	}
}

// TestManagementReviewCAS verifies real authorization, task/entry isolation and
// audit-driven corrections while the parent remains NeedsReview, not completed.
func TestManagementReviewCAS(t *testing.T) {
	a, request, forum := groupFixture(t)
	ctx := context.Background()
	forum.onCreate = func(context.Context) error { return c.ErrDenied }
	if _, err := a.RecoverGroup(ctx, 5, request, joinedDrainer{a.DB}); err != nil {
		t.Fatal(err)
	}
	page, err := a.SupportEntries(ctx, 5, 9, 0, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	entry := page.Items[0]
	operations, err := a.SupportOperations(ctx, 5, 9, entry.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var current rc.SupportOperation
	for _, operation := range operations.Items {
		if operation.ID == entry.OperationID {
			current = operation
		}
	}
	if current.State != "unknown" {
		t.Fatalf("%+v", operations)
	}
	if err = a.DB.Model(&rd.Task{}).Where("id = ?", 9).Update("state", rd.NeedsReview).Error; err != nil {
		t.Fatal(err)
	}
	in := rc.SupportResolution{TaskVersion: page.TaskVersion, Revision: entry.Revision, OperationID: current.ID, OperationVersion: current.Version, Decision: "retry", Evidence: "case:review"}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err == nil {
		t.Fatal("unknown retried")
	}
	in.Decision = "failed"
	wrong := in
	wrong.TaskVersion++
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, wrong); err != rc.ErrStale {
		t.Fatalf("late parent %v", err)
	}
	if _, err = a.SupportOperations(ctx, 5, 999, entry.ID, 0, 10); err == nil {
		t.Fatal("cross task read")
	}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID+999, in); err != rc.ErrNotFound {
		t.Fatalf("cross entry %v", err)
	}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = a.ResolveSupport(ctx, 5, 9, entry.ID, in); err != rc.ErrStale {
		t.Fatalf("late revision %v", err)
	}
	var task rd.Task
	if err = a.DB.First(&task, 9).Error; err != nil || task.State != rd.NeedsReview {
		t.Fatalf("parent forced complete %v", err)
	}
	if err = a.DB.Model(&admin.Admin{}).Where("id = ?", 5).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.SupportEntries(ctx, 5, 9, 0, 10); err == nil {
		t.Fatal("revoked reader")
	}
}

// TestNoTargetProofRequiresHistoricalEmptiness tests the genuine empty case and
// a historical session that prevents a fabricated zero-liability receipt.
func TestNoTargetProofRequiresHistoricalEmptiness(t *testing.T) {
	a, request, _ := moduleFixture(t, 0)
	ctx := context.Background()
	proof, err := a.Run(ctx, request)
	if err != nil || !proof.LocalComplete {
		t.Fatalf("empty %+v %v", proof, err)
	}
	if err = a.DB.Create(&sd.Session{BotID: 1, UserID: 99, ChatID: 99, State: "closed"}).Error; err != nil {
		t.Fatal(err)
	}
	proof, err = a.Run(ctx, request)
	if err != nil || proof.LocalComplete || proof.Remaining == 0 {
		t.Fatalf("history %+v %v", proof, err)
	}
	if err = a.DB.Model(&runtime.GenerationExit{}).Where("bot_id = ?", 1).Update("joined_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.Run(ctx, request); err == nil {
		t.Fatal("missing source drain accepted")
	}
}
