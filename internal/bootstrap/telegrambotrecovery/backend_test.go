package telegrambotrecovery

import (
	"context"
	"errors"
	"fmt"
	broadcast "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	gc "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	groupstore "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	member "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	support "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

// proofDrainer uses the real durable fence store without network or fabricated exit evidence.
type proofDrainer struct{ store rc.RecoveryFenceStore }

func (drainer proofDrainer) BlockAndDrainRecovery(ctx context.Context, fence rc.RecoveryFence) error {
	if err := drainer.store.BlockRecovery(ctx, fence); err != nil {
		return err
	}
	return drainer.store.RecoveryDrained(ctx, fence)
}

// fixture creates a real file-backed SQLite database with exactly one connection.
// Returned authorization flag permits testing revocation on duplicate confirmation and resume.
func fixture(t *testing.T) (*gorm.DB, *app.Service, *bool) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bot.db")), &gorm.Config{})
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
	models := append(tasks.Models(), &rd.Instance{}, &rd.GenerationExit{}, &rd.Contact{}, &rd.Session{}, &rd.Inbox{}, &rd.Delivery{},
		&groups.Target{}, &groups.Route{}, &groups.ModuleConfig{}, &groups.RecoveryFence{}, &groups.Operation{},
		&support.BotPolicy{}, &support.DisasterCheckpoint{}, &broadcast.Broadcast{}, &support.Session{}, &support.TopicOperation{}, &support.TopicMapping{}, &support.ReadyDispatch{}, &support.ReplyMapping{}, &support.ResolutionAudit{},
		&member.AccessInvite{}, &member.AccessJoin{}, &member.AccessOperation{}, &member.AccessMember{}, &member.Reminder{}, &member.IdentityMigration{})
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&rd.Instance{ID: 1, TelegramID: 101, Mode: "internal", Version: 3, Generation: 1, Enabled: true, State: "running"},
		&rd.Instance{ID: 2, TelegramID: 102, Mode: "internal", Version: 7, State: "stopped"},
		&rd.GenerationExit{BotID: 1, Generation: 1, Version: 3},
		&groups.Target{ID: 10, BotInstanceID: 1, TelegramChatID: -1001, OccupancySlot: "chat:-1001", Version: 2, RouteVersion: 4, Purpose: "membership"},
		&groups.Target{ID: 11, BotInstanceID: 1, TelegramChatID: -1002, OccupancySlot: "chat:-1002", Version: 1, RouteVersion: 1, Purpose: "support"},
		&groups.Route{TargetID: 10, BotInstanceID: 1, TelegramChatID: -999, RouteVersion: 3},
		&groups.ModuleConfig{BotInstanceID: 1, Partition: "support", Version: 1, TargetID: 11},
		&rd.Delivery{BotID: 1, Key: "uncertain", Status: "unknown", MessageID: 9007199254740993},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	allowed := true
	service := &app.Service{Store: tasks.New(db), Backend: New(db), Drain: proofDrainer{bots.New(db)}, Authorize: func(_ context.Context, actor uint, action string) error {
		if !allowed || actor != 9 || (action != "bot-preflight" && action != "confirm" && action != "resume") {
			return c.ErrInvalid
		}
		return nil
	}}
	service.Continuation, service.Probe, service.Modules = New(db), &recoveryProbe{db: db}, moduleProofs{}
	return db, service, &allowed
}

// preview creates a persisted inventory under a short deadline that catches single-connection deadlocks.
func preview(t *testing.T, service *app.Service) (*d.Task, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	task, err := service.Preview(ctx, 9, app.Input{SourceBotID: 1, DestinationBotID: 2, RequestKey: "bot-test", Reason: "isolated recovery drill"})
	if err != nil {
		t.Fatal(err)
	}
	return task, ctx
}

// TestFirstConfirmationRequiresExecution proves missing assembly cannot acquire any fence.
// Each case removes one dependency from an otherwise executable synthetic service.
func TestFirstConfirmationRequiresExecution(t *testing.T) {
	for _, dependency := range []string{"drain", "continuation", "probe", "modules"} {
		t.Run(dependency, func(t *testing.T) {
			db, service, _ := fixture(t)
			task, ctx := preview(t, service)
			switch dependency {
			case "drain":
				service.Drain = nil
			case "continuation":
				service.Continuation = nil
			case "probe":
				service.Probe = nil
			case "modules":
				service.Modules = nil
			}
			_, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
			if err == nil {
				t.Fatal("confirmation accepted without", dependency)
			}
			current, err := service.Store.Get(ctx, task.ID)
			if err != nil || current.Task.State != d.AwaitingConfirmation || current.Task.Version != task.Version {
				t.Fatal("blocked confirmation mutated task", current, err)
			}
			for _, model := range []any{&d.ScopeSlot{}, &d.Step{}, &groups.RecoveryFence{}} {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("unexpected persisted fencing %T: %d %v", model, count, err)
				}
			}
			var instances []rd.Instance
			if err := db.Find(&instances).Error; err != nil {
				t.Fatal(err)
			}
			for _, instance := range instances {
				if instance.RecoveryTaskID != 0 {
					t.Fatal("blocked confirmation fenced bot", instance.ID)
				}
			}
		})
	}
}

// TestConfirmedReplayIgnoresLaterAssemblyLoss preserves exact replay without bypassing identity.
// The original reference/version remain authoritative even after the preview expires.
func TestConfirmedReplayIgnoresLaterAssemblyLoss(t *testing.T) {
	db, service, allowed := fixture(t)
	task, ctx := preview(t, service)
	input := c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version}
	confirmed, err := service.Confirm(ctx, task.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	service.Drain, service.Continuation, service.Probe, service.Modules = nil, nil, nil, nil
	if err := db.Model(&d.Task{}).Where("id = ?", task.ID).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Confirm(ctx, task.ID, input)
	if err != nil || replayed.Version != confirmed.Version {
		t.Fatal("accepted confirmation lost replay", replayed, err)
	}
	wrongVersion := input
	wrongVersion.ExpectedVersion++
	if _, err := service.Confirm(ctx, task.ID, wrongVersion); !errors.Is(err, c.ErrStale) {
		t.Fatal("wrong original version replay accepted", err)
	}
	wrongRef := input
	wrongRef.PreflightRef = "wrong-reference"
	if _, err := service.Confirm(ctx, task.ID, wrongRef); !errors.Is(err, c.ErrStale) {
		t.Fatal("wrong reference replay accepted", err)
	}
	*allowed = false
	if _, err := service.Confirm(ctx, task.ID, input); err == nil {
		t.Fatal("revoked confirmation replay accepted")
	}
}

// TestSingleConnectionCrashReplayAndProof preserves unknown delivery, fences and scope after restart.
func TestSingleConnectionCrashReplayAndProof(t *testing.T) {
	db, service, allowed := fixture(t)
	task, ctx := preview(t, service)
	input := c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: task.Version, ActorID: 9}
	confirmed, err := service.Confirm(ctx, task.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct both adapters to simulate a crash after commit but before HTTP response.
	service.Backend = New(db)
	service.Store = tasks.New(db)
	// Dependencies can disappear after confirmation; exact replay must still work.
	service.Continuation, service.Probe, service.Modules = nil, nil, nil
	replay, err := service.Confirm(ctx, task.ID, input)
	if err != nil || replay.ID != confirmed.ID || replay.Version != confirmed.Version {
		t.Fatal("replay", err)
	}
	*allowed = false
	if _, err := service.Confirm(ctx, task.ID, input); err == nil {
		t.Fatal("revoked duplicate accepted")
	}
	if _, err := service.Resume(ctx, task.ID, confirmed.Version, 9); err == nil {
		t.Fatal("revoked resume accepted")
	}
	*allowed = true
	if _, err := service.Resume(ctx, task.ID, confirmed.Version, 9); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("missing exit proof", err)
	}
	if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	advanced, err := service.Resume(ctx, task.ID, confirmed.Version, 9)
	if !errors.Is(err, app.ErrContinuationRequired) || advanced.State != d.Switching {
		t.Fatal("false completion", advanced, err)
	}
	again, err := service.Resume(ctx, task.ID, advanced.Version, 9)
	if !errors.Is(err, app.ErrContinuationRequired) || again.Version != advanced.Version {
		t.Fatal("checkpoint replay", err)
	}
	if _, err := service.Resume(ctx, task.ID, confirmed.Version, 9); !errors.Is(err, c.ErrStale) {
		t.Fatal("stale CAS accepted", err)
	}
	var instances []rd.Instance
	if err := db.Find(&instances).Error; err != nil {
		t.Fatal(err)
	}
	for _, instance := range instances {
		if instance.RecoveryTaskID != task.ID {
			t.Fatal("fence released")
		}
	}
	var delivery rd.Delivery
	if err := db.First(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if delivery.Status != "unknown" || delivery.MessageID != 9007199254740993 {
		t.Fatal("unknown delivery changed")
	}
	var slots int64
	if err := db.Model(&d.ScopeSlot{}).Where("task_id = ?", task.ID).Count(&slots).Error; err != nil || slots != 9 {
		t.Fatal("scope lost", slots, err)
	}
}

// TestConfirmationRollbackAndConcurrentWrites proves transaction rollback and target write exclusion.
func TestConfirmationRollbackAndConcurrentWrites(t *testing.T) {
	db, service, _ := fixture(t)
	task, ctx := preview(t, service)
	input := c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: task.Version, ActorID: 9}
	// Inject a crash at receipt persistence, after task/scope/fence writes in the transaction.
	if err := db.Callback().Create().Before("gorm:create").Register("test_crash", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_recovery_steps" {
			tx.AddError(errors.New("simulated crash"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(ctx, task.ID, input); err == nil {
		t.Fatal("crash accepted")
	}
	if err := db.Callback().Create().Remove("test_crash"); err != nil {
		t.Fatal(err)
	}
	var instance rd.Instance
	if err := db.First(&instance, 2).Error; err != nil || instance.RecoveryTaskID != 0 {
		t.Fatal("partial fence", err)
	}
	current, err := service.Store.Get(ctx, task.ID)
	if err != nil || current.Task.State != d.AwaitingConfirmation {
		t.Fatal("partial task", err)
	}
	if _, err := service.Confirm(ctx, task.ID, input); err != nil {
		t.Fatal(err)
	}
	store := groupstore.New(db).WithBotRecoveryGuard(func(tx *gorm.DB, bot uint) error { return bots.New(tx).GuardRecoveryWrite(ctx, bot) })
	if err := store.Save(ctx, &groups.Target{BotInstanceID: 2, TelegramChatID: -900, Purpose: "membership"}, 0, 0); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("new target bypass", err)
	}
	if err := store.SaveModule(ctx, &groups.ModuleConfig{BotInstanceID: 2, Partition: "membership"}, 0); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("new module bypass", err)
	}
	if err := bots.New(db).SaveContact(ctx, &rd.Contact{BotID: 2, TelegramUserID: 99, ChatID: 99}); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("new contact bypass", err)
	}
	if err := bots.New(db).SaveSession(ctx, &rd.Session{BotID: 2, TelegramUserID: 99, ChatID: 99}, 0); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("new session bypass", err)
	}
	if err := bots.New(db).Enqueue(ctx, &rd.Delivery{BotID: 2, Key: "new"}); !errors.Is(err, rc.ErrRecoveryBlocked) {
		t.Fatal("new delivery bypass", err)
	}
}

// TestConcurrentConfirmCAS serializes two real confirmations on MaxOpenConns=1.
// Exactly one stale-version competitor succeeds; retries cannot create a second parent or fence.
func TestConcurrentConfirmCAS(t *testing.T) {
	db, service, _ := fixture(t)
	task, ctx := preview(t, service)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, version := range []uint64{task.Version, task.Version + 1} {
		go func(expected uint64) {
			<-start
			_, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: expected})
			results <- err
		}(version)
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, c.ErrStale) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	var count int64
	if err := db.Model(&d.Task{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

// recoveryProbe proves HTTP-equivalent work runs outside transactions by querying the same single connection.
// It returns only synthetic permissions; no real Telegram request is made in these tests.
type recoveryProbe struct {
	db     *gorm.DB
	calls  int
	failAt int
}

func (probe *recoveryProbe) ProbeRecovery(ctx context.Context, input gc.RecoveryProbeInput) (groups.Observation, error) {
	probe.calls++
	var count int64
	if err := probe.db.WithContext(ctx).Model(&d.Task{}).Count(&count).Error; err != nil {
		return groups.Observation{}, err
	}
	if probe.failAt == probe.calls {
		return groups.Observation{}, errors.New("interrupted probe")
	}
	now := time.Now().UTC()
	return groups.Observation{BotTelegramID: input.TelegramID, Generation: input.Generation, RouteVersion: input.RouteVersion, State: "ready", Source: "recovery", ChatType: "supergroup", IsForum: true, IsMember: true, MemberStatus: "administrator", CanInviteUsers: true, CanRestrictMembers: true, CanManageTopics: true, ObservedAt: &now}, nil
}

// moduleProofs implements the explicit contract; local retirement alone is intentionally insufficient.
type moduleProofs struct{ remaining uint64 }

func (proof moduleProofs) ContinueBot(context.Context, app.ModuleRequest) ([]app.ModuleProof, error) {
	return []app.ModuleProof{{Module: "membership", Receipt: "membership:1", LocalComplete: true, Remaining: proof.remaining}, {Module: "support", Receipt: "support:1", LocalComplete: true}}, nil
}

// TestMultiTargetInterruptionMatrix resumes crashes before and after each read-only target probe.
// Multiple routes retain the same chat while changing Bot, and no old generation can restart.
func TestMultiTargetInterruptionMatrix(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4, 0} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			db, service, _ := fixture(t)
			task, ctx := preview(t, service)
			confirmation := c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version}
			task, err := service.Confirm(ctx, task.ID, confirmation)
			if err != nil {
				t.Fatal(err)
			}
			if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
				t.Fatal(err)
			}
			probe := &recoveryProbe{db: db, failAt: failAt}
			service.Probe = probe
			service.Continuation = New(db)
			service.Modules = moduleProofs{}
			result, err := service.Resume(ctx, task.ID, task.Version, 9)
			if failAt != 0 {
				if err == nil {
					t.Fatal("expected interruption")
				}
				loaded, loadErr := service.Store.Get(ctx, task.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				service.Continuation = New(db)
				probe.failAt = 0
				result, err = service.Resume(ctx, task.ID, loaded.Task.Version, 9)
			}
			if err != nil || result.State != d.Completed {
				t.Fatal(result, err)
			}
			if _, err := service.Confirm(ctx, task.ID, confirmation); err != nil {
				t.Fatal("confirmation replay", err)
			}
			if _, err := service.Resume(ctx, task.ID, result.Version, 9); err != nil {
				t.Fatal("completion replay", err)
			}
			var receipts []groups.RecoveryFence
			if err := db.Find(&receipts).Error; err != nil || len(receipts) != 2 {
				t.Fatal("parent-target receipts", err)
			}
			for _, row := range receipts {
				if row.TaskID != task.ID || row.ReleasedVersion == 0 || row.OldChatID != row.NewChatID {
					t.Fatal(row)
				}
			}
			old, err := bots.New(db).Get(ctx, 1)
			if err != nil || old.RecoveryTaskID != task.ID {
				t.Fatal("old bot released", err)
			}
			target, err := bots.New(db).Get(ctx, 2)
			if err != nil || target.Enabled || target.RecoveryTaskID != 0 || target.Generation != 0 {
				t.Fatal("target admitted prematurely", err)
			}
		})
	}
}

// TestTargetSwitchCrashRollsBackAtomicReceipt injects failure after route writes but before step commit.
// A new adapter must replay exactly once without incrementing route versions twice.
func TestTargetSwitchCrashRollsBackAtomicReceipt(t *testing.T) {
	db, service, _ := fixture(t)
	task, ctx := preview(t, service)
	task, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	task, err = service.Backend.Checkpoint(ctx, task.ID, task.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("route_crash", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_recovery_steps" {
			tx.AddError(errors.New("route receipt crash"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := New(db).SwitchTarget(ctx, task.ID, task.Version, 10); err == nil {
		t.Fatal("crash not injected")
	}
	if err := db.Callback().Create().Remove("route_crash"); err != nil {
		t.Fatal(err)
	}
	target, err := groupstore.New(db).Get(ctx, 10)
	if err != nil || target.BotInstanceID != 1 || target.Version != 2 || target.RouteVersion != 4 || target.RecoveryTaskID != 0 {
		t.Fatal("partial route commit", target, err)
	}
	for range 2 {
		if err := New(db).SwitchTarget(ctx, task.ID, task.Version, 10); err != nil {
			t.Fatal(err)
		}
	}
	target, err = groupstore.New(db).Get(ctx, 10)
	if err != nil || target.BotInstanceID != 2 || target.Version != 4 || target.RouteVersion != 5 {
		t.Fatal("duplicate switch", target, err)
	}
}

// TestModuleDriftAndRemainingRetainFences checks source CAS before switch and proof gating after switch.
func TestModuleDriftAndRemainingRetainFences(t *testing.T) {
	db, service, _ := fixture(t)
	task, ctx := preview(t, service)
	task, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	task, err = service.Backend.Checkpoint(ctx, task.ID, task.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&groups.ModuleConfig{}).Where("bot_instance_id = ?", 1).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if err := New(db).SwitchTarget(ctx, task.ID, task.Version, 10); !errors.Is(err, c.ErrStale) {
		t.Fatal("module drift ignored", err)
	}
	if err := db.Model(&groups.ModuleConfig{}).Where("bot_instance_id = ?", 1).Update("version", 1).Error; err != nil {
		t.Fatal(err)
	}
	service.Probe = &recoveryProbe{db: db}
	service.Continuation = New(db)
	service.Modules = moduleProofs{remaining: 1}
	task, err = service.Resume(ctx, task.ID, task.Version, 9)
	if !errors.Is(err, app.ErrContinuationRequired) || task.State != d.Recovering {
		t.Fatal(task, err)
	}
	target, err := groupstore.New(db).Get(ctx, 10)
	if err != nil || target.RecoveryTaskID != task.ID {
		t.Fatal("remaining released", err)
	}
	stale := gc.RecoveryObservation{RecoveryProbeInput: gc.RecoveryProbeInput{TaskID: task.ID, TargetID: 10, BotID: 1, TelegramID: 101, ChatID: -1001, RouteVersion: 4}, ExpectedTargetVersion: 2}
	if err := New(db).Observe(ctx, task.ID, task.Version, stale); !errors.Is(err, c.ErrStale) {
		t.Fatal("old late probe accepted", err)
	}
}

// TestExplicitDestinationSwitch verifies immutable chat intent, real route CAS,
// and replay with the existing module-proof test boundary (not a live admission test).
func TestExplicitDestinationSwitch(t *testing.T) {
	db, service, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := service.Preview(ctx, 9, app.Input{SourceBotID: 1, DestinationBotID: 2, RequestKey: "destination", Reason: "synthetic", Routes: []app.RouteDestination{{TargetID: 10, ChatID: -2001}}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.Store.Get(ctx, task.ID)
	if err != nil || detail.Snapshot.BotRoutes[0].DestinationChat() != -2001 {
		t.Fatal("destination missing", err)
	}
	found := false
	for _, chat := range detail.Snapshot.Scope.ChatIDs {
		found = found || chat == "-2001"
	}
	if !found {
		t.Fatal("destination outside task scope")
	}
	task, err = service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	service.Probe, service.Continuation, service.Modules = &recoveryProbe{db: db}, New(db), moduleProofs{}
	result, err := service.Resume(ctx, task.ID, task.Version, 9)
	if err != nil || result.State != d.Completed {
		t.Fatal(result, err)
	}
	target, err := groupstore.New(db).Get(ctx, 10)
	if err != nil || target.TelegramChatID != -2001 || target.BotInstanceID != 2 || target.RouteVersion != 5 {
		t.Fatal("wrong destination", target, err)
	}
	var receipt groups.RecoveryFence
	if err := db.Where("task_id = ? AND target_id = ?", task.ID, 10).Take(&receipt).Error; err != nil || receipt.OldChatID != -1001 || receipt.NewChatID != -2001 {
		t.Fatal("old route lost", receipt, err)
	}
	if _, err := service.Resume(ctx, task.ID, result.Version, 9); err != nil {
		t.Fatal("completed replay", err)
	}
}

// TestDestinationIntentRejected checks user intent and occupancy again at confirmation.
// A stale preview never reserves, overwrites or steals an independently occupied chat.
func TestDestinationIntentRejected(t *testing.T) {
	for _, scenario := range []string{"duplicate", "unknown", "positive", "occupied", "support", "late_occupied"} {
		t.Run(scenario, func(t *testing.T) {
			db, service, _ := fixture(t)
			input := app.Input{SourceBotID: 1, DestinationBotID: 2, RequestKey: scenario, Reason: "synthetic", Routes: []app.RouteDestination{{TargetID: 10, ChatID: -2001}}}
			switch scenario {
			case "duplicate":
				input.Routes = append(input.Routes, input.Routes[0])
			case "unknown":
				input.Routes[0].TargetID = 99
			case "positive":
				input.Routes[0].ChatID = 100
			case "occupied":
				input.Routes[0].ChatID = -1002
			case "support":
				input.Routes[0].TargetID = 11
			}
			ctx := context.Background()
			task, err := service.Preview(ctx, 9, input)
			if scenario != "late_occupied" {
				if err == nil {
					t.Fatal("invalid destination accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&groups.Target{ID: 90, TelegramChatID: -2001, OccupancySlot: "chat:-2001", BotInstanceID: 3, Purpose: "membership"}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, ExpectedVersion: task.Version, PreflightRef: task.PreflightRef}); err == nil {
				t.Fatal("late occupancy ignored")
			}
		})
	}
}

// TestPreflightRejectsDriftAndExistingTarget verifies exact all-route CAS, not just requested IDs.
func TestPreflightRejectsDriftAndExistingTarget(t *testing.T) {
	db, service, _ := fixture(t)
	task, ctx := preview(t, service)
	if err := db.Model(&groups.Target{}).Where("id = ?", 11).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(ctx, task.ID, c.Confirmation{PreflightRef: task.PreflightRef, ExpectedVersion: task.Version, ActorID: 9}); !errors.Is(err, c.ErrStale) {
		t.Fatal("disabled route drift ignored", err)
	}
	if err := db.Create(&rd.Contact{BotID: 2, TelegramUserID: 88, ChatID: 88}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backend.Snapshot(ctx, 1, 2); !errors.Is(err, c.ErrConflict) {
		t.Fatal("existing target business accepted", err)
	}
}

// TestLocalAtomicCheckpointFailures proves drain/config/release have no separate
// receipt-loss window: failed receipts roll back effects before the original resume.
func TestLocalAtomicCheckpointFailures(t *testing.T) {
	for _, phase := range []string{"drain", "config", "release"} {
		t.Run(phase, func(t *testing.T) {
			db, service, _ := fixture(t)
			task, ctx := preview(t, service)
			task, err := service.Confirm(ctx, task.ID, c.Confirmation{ActorID: 9, PreflightRef: task.PreflightRef, ExpectedVersion: task.Version})
			if err != nil {
				t.Fatal(err)
			}
			if err := bots.New(db).RecordGenerationExit(ctx, 1, 3, 1); err != nil {
				t.Fatal(err)
			}
			service.Probe, service.Continuation, service.Modules = &recoveryProbe{db: db}, New(db), moduleProofs{}
			callback := "local_checkpoint_crash"
			if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				step, ok := tx.Statement.Dest.(*d.Step)
				if ok && ((phase == "drain" && step.BusinessKey == "bot.generations_joined") || (phase == "config" && step.BusinessKey == "bot.modules_moved")) {
					tx.AddError(errors.New("synthetic checkpoint crash"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				fields, ok := tx.Statement.Dest.(map[string]any)
				if phase == "release" && ok && tx.Statement.Table == "telegram_recovery_tasks" && fields["state"] == d.Completed {
					tx.AddError(errors.New("synthetic finalization crash"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = service.Resume(ctx, task.ID, task.Version, 9); err == nil {
				t.Fatal("interruption not injected")
			}
			if err := db.Callback().Create().Remove(callback); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Remove(callback); err != nil {
				t.Fatal(err)
			}
			loaded, err := service.Store.Get(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]d.State{"drain": d.Fencing, "config": d.Switching, "release": d.Recovering}[phase]
			if loaded.Task.State != expected {
				t.Fatal("partial transaction committed", loaded.Task.State)
			}
			var unresolved int64
			if err := db.Model(&d.Step{}).Where("task_id = ? AND state <> ?", task.ID, d.StepSucceeded).Count(&unresolved).Error; err != nil || unresolved != 0 {
				t.Fatal("leaked intermediate step", unresolved, err)
			}
			if phase == "config" {
				var count int64
				if err := db.Model(&groups.ModuleConfig{}).Where("bot_instance_id = ?", 2).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("config escaped rollback", count, err)
				}
			}
			if phase == "release" {
				var released int64
				if err := db.Model(&groups.RecoveryFence{}).Where("task_id = ? AND released_version <> 0", task.ID).Count(&released).Error; err != nil || released != 0 {
					t.Fatal("release escaped rollback", released, err)
				}
			}
			service.Backend, service.Continuation = New(db), New(db)
			result, err := service.Resume(ctx, task.ID, loaded.Task.Version, 9)
			if err != nil || result.State != d.Completed {
				t.Fatal(result, err)
			}
			if _, err = service.Resume(ctx, task.ID, result.Version, 9); err != nil {
				t.Fatal("duplicate resume", err)
			}
			old, err := bots.New(db).Get(ctx, 1)
			if err != nil || old.RecoveryTaskID != task.ID {
				t.Fatal("old fence released", err)
			}
		})
	}
}
