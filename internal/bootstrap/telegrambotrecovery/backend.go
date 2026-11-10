// Package telegrambotrecovery assembles the database-only safety stage of cross-Bot recovery.
package telegrambotrecovery

import (
	"context"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"strconv"
	"time"
)

// Backend owns no global connection; all nested repositories receive the current transaction.
type Backend struct{ db *gorm.DB }

// New returns a local adapter without migrating, issuing HTTP, or enabling business modules.
func New(db *gorm.DB) *Backend { return &Backend{db: db} }

// Snapshot reads both identities and every source route/module in one consistent transaction.
// A previously active destination is deliberately unsupported in this safety stage.
func (backend *Backend) Snapshot(ctx context.Context, source, destination uint) (d.Snapshot, error) {
	var snapshot d.Snapshot
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = readSnapshot(tx, source, destination, 0)
		return err
	})
	return snapshot, err
}

// SnapshotRoutes validates explicit destination intent in the same consistent snapshot.
// It returns a server-derived scope; no user-supplied source identity or version is trusted.
func (backend *Backend) SnapshotRoutes(ctx context.Context, source, destination uint, routes []app.RouteDestination) (d.Snapshot, error) {
	var snapshot d.Snapshot
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = readSnapshot(tx, source, destination, 0)
		if err != nil {
			return err
		}
		return applyDestinations(tx, &snapshot, routes)
	})
	return snapshot, err
}

// applyDestinations validates bounded unique target/chat intent against real occupancy.
// Only current membership routes can move chats; old snapshots without intent remain unchanged.
func applyDestinations(tx *gorm.DB, snapshot *d.Snapshot, requested []app.RouteDestination) error {
	if len(requested) > 100 {
		return c.ErrInvalid
	}
	intent := make(map[uint]int64, len(requested))
	for _, route := range requested {
		if route.TargetID == 0 || route.ChatID >= 0 || intent[route.TargetID] != 0 {
			return c.ErrInvalid
		}
		intent[route.TargetID] = route.ChatID
	}
	used := make(map[int64]bool)
	for index := range snapshot.BotRoutes {
		route := &snapshot.BotRoutes[index]
		chat, explicit := intent[route.TargetID]
		if explicit {
			if route.Purpose != "membership" && chat != route.ChatID {
				return c.ErrInvalid
			}
			route.NewChatID = chat
			delete(intent, route.TargetID)
		} else {
			chat = route.ChatID
		}
		if used[chat] {
			return c.ErrConflict
		}
		used[chat] = true
		if chat != route.ChatID {
			var occupied int64
			if err := tx.Model(&groups.Target{}).Where("telegram_chat_id = ? OR occupancy_slot = ?", chat, "chat:"+strconv.FormatInt(chat, 10)).Count(&occupied).Error; err != nil {
				return err
			}
			if occupied != 0 {
				return c.ErrConflict
			}
			snapshot.Scope.ChatIDs = append(snapshot.Scope.ChatIDs, strconv.FormatInt(chat, 10))
		}
	}
	if len(intent) != 0 {
		return c.ErrInvalid
	}
	return nil
}

// reapplyDestinations revalidates saved intent before confirmation/drain checkpoint.
// It never accepts a new destination after the immutable preflight was created.
func reapplyDestinations(tx *gorm.DB, current *d.Snapshot, saved d.Snapshot) error {
	var intent []app.RouteDestination
	for _, route := range saved.BotRoutes {
		if route.NewChatID != 0 {
			intent = append(intent, app.RouteDestination{TargetID: route.TargetID, ChatID: route.NewChatID})
		}
	}
	return applyDestinations(tx, current, intent)
}

// readSnapshot locks both runtime parents in deterministic order and derives scope from storage.
// owner allows only exact same-task fences during confirmation replay and checkpoint verification.
func readSnapshot(tx *gorm.DB, source, destination, owner uint) (d.Snapshot, error) {
	snapshot := d.Snapshot{}
	if source == 0 || destination == 0 || source == destination {
		return snapshot, c.ErrInvalid
	}
	var instances []rd.Instance
	// UPDATE obtains SQLite's writer lock before reads and is portable to PostgreSQL.
	if err := tx.Model(&rd.Instance{}).Where("id IN ?", []uint{source, destination}).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
		return snapshot, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", []uint{source, destination}).Order("id ASC").Find(&instances).Error; err != nil {
		return snapshot, err
	}
	if len(instances) != 2 {
		return snapshot, c.ErrNotFound
	}
	var old, newBot rd.Instance
	for _, instance := range instances {
		if instance.ID == source {
			old = instance
		} else {
			newBot = instance
		}
	}
	if old.TelegramID <= 0 || newBot.TelegramID <= 0 || old.TelegramID == newBot.TelegramID || old.Mode != "internal" || newBot.Mode != "internal" || old.Version == 0 || newBot.Version == 0 {
		return snapshot, c.ErrInvalid
	}
	// No claim of target platform capability: a disabled, never-admitted destination
	// can be fenced safely, but route/module execution still needs a later live preflight.
	if newBot.Enabled || newBot.Generation != 0 || newBot.Offset != 0 || newBot.State != "stopped" {
		return snapshot, c.ErrConflict
	}
	for _, instance := range instances {
		if instance.RecoveryTaskID != 0 && instance.RecoveryTaskID != owner {
			return snapshot, c.ErrScopeBusy
		}
	}
	if err := destinationEmpty(tx, destination); err != nil {
		return snapshot, err
	}
	snapshot.Old = d.Endpoint{BotID: source, TelegramID: old.TelegramID}
	snapshot.New = d.Endpoint{BotID: destination, TelegramID: newBot.TelegramID}
	snapshot.ExpectedOld = d.Versions{Config: old.Version, Generation: old.Generation}
	snapshot.ExpectedNew = d.Versions{Config: newBot.Version, Generation: newBot.Generation}
	snapshot.Scope = d.Scope{BotIDs: []string{strconv.FormatUint(uint64(source), 10), strconv.FormatUint(uint64(destination), 10)}, TelegramIDs: []string{strconv.FormatInt(old.TelegramID, 10), strconv.FormatInt(newBot.TelegramID, 10)}}
	if err := inventory(tx, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// inventory includes disabled targets and all source modules; too-large scopes fail, never truncate.
func inventory(tx *gorm.DB, snapshot *d.Snapshot) error {
	var targets []groups.Target
	if err := tx.Where("bot_instance_id = ?", snapshot.Old.BotID).Order("id ASC").Find(&targets).Error; err != nil {
		return err
	}
	if len(targets) == 0 || len(targets) > 100 {
		return c.ErrInvalid
	}
	for _, target := range targets {
		if (target.Purpose != "membership" && target.Purpose != "support") || target.RecoveryTaskID != 0 || target.Version == 0 || target.RouteVersion == 0 || target.TelegramChatID >= 0 {
			return c.ErrConflict
		}
		snapshot.BotRoutes = append(snapshot.BotRoutes, d.BotRoute{TargetID: target.ID, ChatID: target.TelegramChatID, Version: target.Version, RouteVersion: target.RouteVersion, Purpose: target.Purpose, Enabled: target.Enabled})
		snapshot.Scope.TargetIDs = append(snapshot.Scope.TargetIDs, strconv.FormatUint(uint64(target.ID), 10))
		snapshot.Scope.ChatIDs = append(snapshot.Scope.ChatIDs, strconv.FormatInt(target.TelegramChatID, 10))
	}
	var history []groups.Route
	if err := tx.Where("bot_instance_id = ?", snapshot.Old.BotID).Order("target_id ASC, route_version ASC").Find(&history).Error; err != nil {
		return err
	}
	if len(history) > 100 {
		return c.ErrInvalid
	}
	for _, route := range history {
		snapshot.BotHistory = append(snapshot.BotHistory, d.BotRoute{TargetID: route.TargetID, ChatID: route.TelegramChatID, RouteVersion: route.RouteVersion})
		snapshot.Scope.TargetIDs = append(snapshot.Scope.TargetIDs, strconv.FormatUint(uint64(route.TargetID), 10))
		snapshot.Scope.ChatIDs = append(snapshot.Scope.ChatIDs, strconv.FormatInt(route.TelegramChatID, 10))
	}
	var modules []groups.ModuleConfig
	if err := tx.Where("bot_instance_id = ?", snapshot.Old.BotID).Order("partition ASC").Find(&modules).Error; err != nil {
		return err
	}
	for _, module := range modules {
		if module.Partition != "membership" && module.Partition != "support" {
			return c.ErrInvalid
		}
		if module.TargetID != 0 {
			found := false
			for _, route := range snapshot.BotRoutes {
				if route.TargetID == module.TargetID && route.Purpose == module.Partition {
					found = true
				}
			}
			if !found {
				return c.ErrStale
			}
		}
		snapshot.BotModules = append(snapshot.BotModules, d.BotModule{Partition: module.Partition, Version: module.Version, TargetID: module.TargetID, Enabled: module.Enabled})
	}
	return nil
}

// Confirm rechecks immutable preflight, then atomically confirms and fences both instances.
// Same actor/version/ref replay is authorized by the application before entering this method.
func (backend *Backend) Confirm(ctx context.Context, id uint, input c.Confirmation) (*d.Task, error) {
	var result *d.Task
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := tasks.New(tx)
		detail, err := store.Get(ctx, id)
		if err != nil {
			return err
		}
		if detail.Task.Kind != d.BotReplace || detail.Task.PreflightRef != input.PreflightRef {
			return c.ErrStale
		}
		if detail.Task.ConfirmedAt != nil {
			result, err = store.Confirm(ctx, input)
			if err != nil {
				return err
			}
			if result.State == d.Completed {
				return nil
			}
			for _, fence := range fences(id, detail.Snapshot) {
				if err := bots.New(tx).BlockRecovery(ctx, fence); err != nil {
					return err
				}
			}
			return nil
		}
		current, err := readSnapshot(tx, detail.Snapshot.Old.BotID, detail.Snapshot.New.BotID, id)
		if err != nil {
			return err
		}
		if err := reapplyDestinations(tx, &current, detail.Snapshot); err != nil {
			return err
		}
		if !sameSnapshot(current, detail.Snapshot) {
			return c.ErrStale
		}
		result, err = store.Confirm(ctx, input)
		if err != nil {
			return err
		}
		for _, fence := range fences(id, current) {
			if err := bots.New(tx).BlockRecovery(ctx, fence); err != nil {
				return err
			}
		}
		return receipt(ctx, store, id, "bot.fences_installed")
	})
	return result, err
}

// sameSnapshot compares server-derived facts independent of normalized scope ordering.
func sameSnapshot(current, saved d.Snapshot) bool {
	current.Scope, saved.Scope = d.Scope{}, d.Scope{}
	return reflect.DeepEqual(current, saved)
}

// fences returns both exact immutable runtime ownership tuples; neither is ever released here.
func fences(id uint, snapshot d.Snapshot) []runtime.RecoveryFence {
	return []runtime.RecoveryFence{{BotID: snapshot.Old.BotID, TaskID: id, Version: snapshot.ExpectedOld.Config, Generation: snapshot.ExpectedOld.Generation}, {BotID: snapshot.New.BotID, TaskID: id, Version: snapshot.ExpectedNew.Config, Generation: snapshot.ExpectedNew.Generation}}
}

// Checkpoint verifies actual durable exit proofs again using tx, then writes an atomic receipt.
// It stops at switching; no route, module, historical order or unknown delivery is modified.
func (backend *Backend) Checkpoint(ctx context.Context, id uint, version uint64) (*d.Task, error) {
	var result *d.Task
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := tasks.New(tx)
		detail, err := store.Get(ctx, id)
		if err != nil {
			return err
		}
		if detail.Task.Kind != d.BotReplace || detail.Task.Version != version || (detail.Task.State != d.Fencing && detail.Task.State != d.Switching) {
			return c.ErrStale
		}
		current, err := readSnapshot(tx, detail.Snapshot.Old.BotID, detail.Snapshot.New.BotID, id)
		if err != nil {
			return err
		}
		if err := reapplyDestinations(tx, &current, detail.Snapshot); err != nil {
			return err
		}
		if !sameSnapshot(current, detail.Snapshot) {
			return c.ErrStale
		}
		for _, fence := range fences(id, detail.Snapshot) {
			if err := bots.New(tx).RecoveryDrained(ctx, fence); err != nil {
				return err
			}
		}
		if err := receipt(ctx, store, id, "bot.generations_joined"); err != nil {
			return err
		}
		if detail.Task.State == d.Switching {
			result = &detail.Task
			return nil
		}
		result, err = store.Transition(ctx, c.Transition{TaskID: id, ExpectedVersion: version, State: d.Switching})
		return err
	})
	return result, err
}

// receipt is local-only and atomic with its caller transaction, so crash replay has no ambiguity.
func receipt(ctx context.Context, store c.Store, id uint, key string) error {
	step, err := store.EnsureStep(ctx, c.StepInput{TaskID: id, BusinessKey: key, Operation: key})
	if err != nil {
		return err
	}
	if step.State == d.StepSucceeded {
		return nil
	}
	step, err = store.ClaimStep(ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
	if err != nil {
		return err
	}
	_, err = store.CompleteStep(ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepSucceeded, ResultCode: "verified"})
	return err
}
