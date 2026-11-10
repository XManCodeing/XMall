package gormstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	"gorm.io/gorm"
)

// recoverySetup builds an enabled, fully observed target on SQLite's one connection.
func recoverySetup(t *testing.T) (*a.Service, *s.Store, *gorm.DB, c.RecoveryInput) {
	t.Helper()
	service, store, db, platform := setup(t)
	if err := db.AutoMigrate(&d.RecoveryFence{}); err != nil {
		t.Fatal(err)
	}
	platform.result.Observation.IsMember = true
	ctx := context.Background()
	target := create(t, service)
	if _, err := service.Check(ctx, target.ID, target.RouteVersion, 1, "manual"); err != nil {
		t.Fatal(err)
	}
	row, err := store.Get(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.Enabled = true
	if err = store.Save(ctx, row, row.Version, row.RouteVersion); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveModule(ctx, &d.ModuleConfig{BotInstanceID: 1, Partition: "support", TargetID: row.ID, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	return service, store, db, c.RecoveryInput{TargetID: row.ID, TaskID: 71, ExpectedVersion: row.Version, ExpectedRouteVersion: row.RouteVersion,
		OldBotID: row.BotInstanceID, OldChatID: row.TelegramChatID, NewBotID: 2, NewChatID: -100777}
}

// legacyRecoveryReceipt models the v21 single-parent key only for the upgrade regression.
type legacyRecoveryReceipt struct{ d.RecoveryFence }

func (legacyRecoveryReceipt) TableName() string { return "telegram_group_recovery_fences" }

// TestRecoveryTargetReceiptUpgrade copies legacy receipts, replays safely and preserves release proof.
func TestRecoveryTargetReceiptUpgrade(t *testing.T) {
	_, _, db, _ := setup(t)
	if err := db.AutoMigrate(&legacyRecoveryReceipt{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX legacy_recovery_task ON telegram_group_recovery_fences(task_id)").Error; err != nil {
		t.Fatal(err)
	}
	old := d.RecoveryFence{TaskID: 51, TargetID: 9, OldBotID: 1, NewBotID: 2, OldChatID: -1, NewChatID: -2, InitialVersion: 3, InitialRouteVersion: 4, FrozenVersion: 4, ReplacedVersion: 5, ReleasedVersion: 6, ReleasedGeneration: 7, ReleasedObservationSeq: 8, ReleasedBotTelegramID: 999}
	if err := db.Table("telegram_group_recovery_fences").Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.MigrateRecoveryTargets(db); err != nil {
			t.Fatal(err)
		}
	}
	var copied d.RecoveryFence
	if err := db.Where("task_id = ? AND target_id = ?", 51, 9).Take(&copied).Error; err != nil || copied != old {
		t.Fatal(copied, err)
	}
	second := old
	second.TargetID = 10
	if err := db.Create(&second).Error; err != nil {
		t.Fatal("second target rejected", err)
	}
	var count int64
	if err := db.Model(&d.RecoveryFence{}).Where("task_id = ?", 51).Count(&count).Error; err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

// TestRecoveryFenceCannotBeBypassed covers direct repository callers, not only UI validation.
func TestRecoveryFenceCannotBeBypassed(t *testing.T) {
	service, store, _, request := recoverySetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frozen, err := store.FreezeTarget(ctx, request)
	if err != nil || !frozen.Enabled || frozen.RecoveryTaskID != request.TaskID {
		t.Fatalf("freeze: %+v %v", frozen, err)
	}
	if service.View(*frozen).Fresh {
		t.Fatal("fenced target reported fresh")
	}
	if _, err = service.ResolveSupportRoute(ctx, 1); !errors.Is(err, c.ErrCapability) {
		t.Fatal("fenced support route resolved", err)
	}
	modules, err := store.Modules(ctx, 1)
	if err != nil || len(modules) != 1 || modules[0].Enabled || modules[0].EffectiveEnabled || !modules[0].ConfiguredEnabled {
		t.Fatalf("effective module flag: %+v %v", modules, err)
	}
	for _, mutation := range []func(*d.Target){
		func(row *d.Target) { row.Enabled = false },
		func(row *d.Target) { row.TelegramChatID = -100888 },
		func(row *d.Target) { row.BotInstanceID = 8 },
		func(row *d.Target) { row.Purpose = "membership" },
	} {
		candidate := *frozen
		candidate.RecoveryTaskID = 0 // A forged ordinary PATCH must not clear ownership.
		mutation(&candidate)
		if err = store.Save(ctx, &candidate, frozen.Version, frozen.RouteVersion); !errors.Is(err, c.ErrConflict) {
			t.Fatal("ordinary Save bypassed fence", err)
		}
	}
	for _, candidate := range []d.ModuleConfig{
		{BotInstanceID: 1, Partition: "support", TargetID: frozen.ID, Enabled: true},
		{BotInstanceID: 1, Partition: "support", TargetID: 0, Enabled: false},
		{BotInstanceID: 9, Partition: "support", TargetID: frozen.ID, Enabled: false},
	} {
		if err = store.SaveModule(ctx, &candidate, 1); !errors.Is(err, c.ErrConflict) {
			t.Fatal("ordinary SaveModule bypassed fence", err)
		}
	}
	other := request
	other.TaskID++
	other.ExpectedVersion = frozen.Version
	if _, err = store.FreezeTarget(ctx, other); !errors.Is(err, c.ErrConflict) {
		t.Fatal("another task acquired fence", err)
	}
}

// TestRecoveryInterruptedReplaySingleConnection exercises every commit/retry
// boundary and stale old-route results after process/store reconstruction.
func TestRecoveryInterruptedReplaySingleConnection(t *testing.T) {
	service, store, db, request := recoverySetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frozen, err := store.FreezeTarget(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	store = s.New(db)
	repeated, err := store.FreezeTarget(ctx, request)
	if err != nil || repeated.Version != frozen.Version {
		t.Fatal("freeze replay", err)
	}
	_, oldOperation, err := store.BeginCheck(ctx, frozen.ID, frozen.RouteVersion, "manual", 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	replace := request
	replace.ExpectedVersion = frozen.Version
	replaced, err := store.ReplaceRoute(ctx, replace)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced.CrossBot || replaced.ModuleConfigurationMoved || !replaced.Target.Enabled || replaced.Target.Purpose != "support" {
		t.Fatalf("changed intent or moved modules: %+v", replaced)
	}
	observation := replaced.Target.Observation
	if observation != (d.Observation{State: "unknown", ErrorClass: "route_changed"}) {
		t.Fatalf("old evidence retained: %+v", observation)
	}
	now := time.Now().UTC()
	applied, err := store.FinishCheck(ctx, oldOperation, d.Observation{RouteVersion: frozen.RouteVersion, Sequence: oldOperation.Sequence, State: "ready", ObservedAt: &now}, now)
	if err != nil || applied {
		t.Fatal("late old-route response applied", err)
	}
	store = s.New(db)
	replayed, err := store.ReplaceRoute(ctx, replace)
	if err != nil || replayed.Target.Version != replaced.Target.Version {
		t.Fatal("replace replay", err)
	}
	if _, err = store.FreezeTarget(ctx, request); err != nil {
		t.Fatal("freeze replay after replacement", err)
	}
	detail, err := service.Check(ctx, request.TargetID, replaced.Target.RouteVersion, 1, "manual")
	if err != nil || detail.Fresh {
		t.Fatal("check must retain fence", err)
	}
	release := releaseInput(detail.Target, request.TaskID)
	released, err := store.ReleaseRecovery(ctx, release)
	if err != nil || released.RecoveryTaskID != 0 || !released.Enabled {
		t.Fatalf("release: %+v %v", released, err)
	}
	store = s.New(db)
	if repeated, err = store.ReleaseRecovery(ctx, release); err != nil || repeated.Version != released.Version {
		t.Fatal("release replay", err)
	}
	if _, err = store.FreezeTarget(ctx, request); !errors.Is(err, c.ErrConflict) {
		t.Fatal("completed task acquired fence again", err)
	}
	routes, _, err := store.History(ctx, request.TargetID)
	if err != nil || len(routes) != 2 || routes[0].RetiredAt != nil || routes[1].RetiredAt == nil {
		t.Fatalf("history: %+v %v", routes, err)
	}
	var configs []d.ModuleConfig
	if err = db.Find(&configs).Error; err != nil || len(configs) != 1 || configs[0].BotInstanceID != request.OldBotID {
		t.Fatalf("module automatically migrated: %+v %v", configs, err)
	}
	next := c.RecoveryInput{TargetID: released.ID, TaskID: 72, ExpectedVersion: released.Version, ExpectedRouteVersion: released.RouteVersion,
		OldBotID: released.BotInstanceID, OldChatID: released.TelegramChatID, NewBotID: released.BotInstanceID, NewChatID: -100999}
	if _, err = store.FreezeTarget(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReleaseRecovery(ctx, release); !errors.Is(err, c.ErrConflict) {
		t.Fatal("old release erased newer fence", err)
	}
}

// TestRecoveryPreservesDisabledIntent permits cross-Bot ownership of the same
// physical chat while retaining the saved disabled intent after successful release.
func TestRecoveryPreservesDisabledIntent(t *testing.T) {
	service, store, _, request := recoverySetup(t)
	ctx := context.Background()
	target, err := store.Get(ctx, request.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	target.Enabled = false
	if err = store.Save(ctx, target, target.Version, target.RouteVersion); err != nil {
		t.Fatal(err)
	}
	request.ExpectedVersion = target.Version
	request.NewChatID = request.OldChatID
	frozen, err := store.FreezeTarget(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedVersion = frozen.Version
	replaced, err := store.ReplaceRoute(ctx, request)
	if err != nil || !replaced.CrossBot || replaced.Target.TelegramChatID != request.OldChatID {
		t.Fatal("same-chat Bot replacement", err)
	}
	detail, err := service.Check(ctx, target.ID, replaced.Target.RouteVersion, 1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	released, err := store.ReleaseRecovery(ctx, releaseInput(detail.Target, request.TaskID))
	if err != nil || released.Enabled || released.RecoveryTaskID != 0 {
		t.Fatalf("disabled intent not preserved: %+v %v", released, err)
	}
}

// releaseInput snapshots all release CAS and runtime identity fields.
func releaseInput(target d.Target, taskID uint) c.RecoveryRelease {
	return c.RecoveryRelease{TargetID: target.ID, TaskID: taskID, ExpectedVersion: target.Version, ExpectedRouteVersion: target.RouteVersion,
		ExpectedObservationSeq: target.ObservationSeq, BotID: target.BotInstanceID, ChatID: target.TelegramChatID,
		Generation: target.Observation.Generation, BotTelegramID: target.Observation.BotTelegramID}
}

// TestRecoveryOccupancyAndStaleCAS verifies rollback and strict preflight snapshots.
func TestRecoveryOccupancyAndStaleCAS(t *testing.T) {
	_, store, _, request := recoverySetup(t)
	ctx := context.Background()
	occupied := d.Target{Name: "occupied", Purpose: "membership", BotInstanceID: 44, TelegramChatID: request.NewChatID}
	if err := store.Save(ctx, &occupied, 0, 0); err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.ExpectedVersion--
	if _, err := store.FreezeTarget(ctx, stale); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale preflight accepted", err)
	}
	frozen, err := store.FreezeTarget(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRoute(ctx, request); !errors.Is(err, c.ErrConflict) {
		t.Fatal("pre-freeze version accepted", err)
	}
	request.ExpectedVersion = frozen.Version
	if _, err = store.ReplaceRoute(ctx, request); !errors.Is(err, c.ErrConflict) {
		t.Fatal("occupied physical chat accepted across bots", err)
	}
	unchanged, err := store.Get(ctx, frozen.ID)
	if err != nil || unchanged.RouteVersion != frozen.RouteVersion || unchanged.TelegramChatID != frozen.TelegramChatID || unchanged.RecoveryTaskID != frozen.RecoveryTaskID {
		t.Fatal("occupancy conflict changed target", err)
	}
	routes, _, err := store.History(ctx, frozen.ID)
	if err != nil || len(routes) != 1 || routes[0].RetiredAt != nil {
		t.Fatal("occupancy conflict retired old route", err)
	}
}

// TestRecoveryReleaseCapabilities refuses stale, incomplete and wrong-version observations.
func TestRecoveryReleaseCapabilities(t *testing.T) {
	service, store, db, request := recoverySetup(t)
	request.NewBotID = request.OldBotID // Same-Bot group replacement is supported.
	ctx := context.Background()
	frozen, err := store.FreezeTarget(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedVersion = frozen.Version
	replaced, err := store.ReplaceRoute(ctx, request)
	if err != nil || replaced.CrossBot {
		t.Fatal("same-Bot replacement", err)
	}
	detail, err := service.Check(ctx, frozen.ID, replaced.Target.RouteVersion, 1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	release := releaseInput(detail.Target, request.TaskID)
	stale := release
	stale.ExpectedObservationSeq++
	if _, err = store.ReleaseRecovery(ctx, stale); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale observation CAS", err)
	}
	for _, fields := range []map[string]any{
		{"obs_can_manage_topics": false},
		{"obs_can_manage_topics": true, "obs_is_member": false},
		{"obs_is_member": true, "obs_observed_at": time.Now().Add(-6 * time.Minute)},
		{"obs_observed_at": time.Now().Add(time.Minute)},
		{"obs_observed_at": time.Now(), "obs_generation": 9},
	} {
		if err = db.Model(&d.Target{}).Where("id = ?", frozen.ID).Updates(fields).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = store.ReleaseRecovery(ctx, release); !errors.Is(err, c.ErrCapability) {
			t.Fatal("invalid capability released fence", fields, err)
		}
	}
}

// TestRecoveryInputIdentity requires explicit, distinct route pairs on all starts.
func TestRecoveryInputIdentity(t *testing.T) {
	_, store, _, request := recoverySetup(t)
	for _, mutate := range []func(*c.RecoveryInput){
		func(in *c.RecoveryInput) { in.OldBotID = 0 },
		func(in *c.RecoveryInput) { in.OldChatID = 0 },
		func(in *c.RecoveryInput) { in.NewBotID = 0 },
		func(in *c.RecoveryInput) { in.NewChatID = 0 },
		func(in *c.RecoveryInput) { in.NewBotID, in.NewChatID = in.OldBotID, in.OldChatID },
	} {
		invalid := request
		mutate(&invalid)
		if _, err := store.FreezeTarget(context.Background(), invalid); !errors.Is(err, c.ErrInvalid) {
			t.Fatal("invalid identity accepted", err)
		}
	}
}
