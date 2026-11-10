package telegrambotrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	routes "github.com/XManCodeing/XMall/internal/modules/telegram/groups/infrastructure/gormstore"
	app "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	"gorm.io/gorm"
	"reflect"
	"sort"
	"time"
)

// ownedTask rechecks exact parent version and real drain proofs using the transaction handle.
// Scope cardinality and all source module versions are checked before each route mutation.
func ownedTask(ctx context.Context, tx *gorm.DB, id uint, version uint64) (*c.Detail, error) {
	detail, err := tasks.New(tx).Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail.Task.Kind != d.BotReplace || detail.Task.Version != version || (detail.Task.State != d.Switching && detail.Task.State != d.Recovering) {
		return nil, c.ErrStale
	}
	for _, fence := range fences(id, detail.Snapshot) {
		if err := bots.New(tx).RecoveryDrained(ctx, fence); err != nil {
			return nil, err
		}
	}
	destination, err := bots.New(tx).Get(ctx, detail.Snapshot.New.BotID)
	if err != nil {
		return nil, err
	}
	if destination.Enabled || destination.TelegramID != detail.Snapshot.New.TelegramID || destination.Mode != "internal" {
		return nil, c.ErrStale
	}
	var slots []d.ScopeSlot
	if err := tx.Where("task_id = ?", id).Find(&slots).Error; err != nil {
		return nil, err
	}
	actual := make([]string, 0, len(slots))
	for _, slot := range slots {
		actual = append(actual, slot.Key)
	}
	expected := []string{}
	for prefix, values := range map[string][]string{"bot": detail.Snapshot.Scope.BotIDs, "target": detail.Snapshot.Scope.TargetIDs, "chat": detail.Snapshot.Scope.ChatIDs, "telegram": detail.Snapshot.Scope.TelegramIDs} {
		for _, value := range values {
			expected = append(expected, prefix+":"+value)
		}
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		return nil, c.ErrStale
	}
	var count int64
	if err := tx.Model(&gd.Target{}).Where("bot_instance_id IN ?", []uint{detail.Snapshot.Old.BotID, detail.Snapshot.New.BotID}).Count(&count).Error; err != nil {
		return nil, err
	}
	if int(count) != len(detail.Snapshot.BotRoutes) {
		return nil, c.ErrStale
	}
	if detail.Task.State == d.Switching {
		if err := checkModules(tx, detail.Snapshot, false); err != nil {
			return nil, err
		}
	}
	return detail, nil
}

// checkModules compares exact source/destination configuration sets, not only enabled rows.
// moved=true validates retired source and copied target intent after the atomic move.
func checkModules(tx *gorm.DB, snapshot d.Snapshot, moved bool) error {
	var rows []gd.ModuleConfig
	if err := tx.Where("bot_instance_id IN ?", []uint{snapshot.Old.BotID, snapshot.New.BotID}).Order("partition ASC").Find(&rows).Error; err != nil {
		return err
	}
	source, destination := []d.BotModule{}, []d.BotModule{}
	for _, row := range rows {
		item := d.BotModule{Partition: row.Partition, Version: row.Version, TargetID: row.TargetID, Enabled: row.Enabled}
		if row.BotInstanceID == snapshot.Old.BotID {
			source = append(source, item)
		} else {
			destination = append(destination, item)
		}
	}
	expected := append([]d.BotModule{}, snapshot.BotModules...)
	if moved {
		retired := append([]d.BotModule{}, expected...)
		for index := range retired {
			retired[index].Version++
			retired[index].Enabled = false
			retired[index].TargetID = 0
		}
		for index := range expected {
			expected[index].Version = 1
		}
		if !reflect.DeepEqual(source, retired) || !reflect.DeepEqual(destination, expected) {
			return c.ErrStale
		}
	} else if !reflect.DeepEqual(source, expected) || len(destination) != 0 {
		return c.ErrStale
	}
	return nil
}

// SwitchTarget atomically freezes/replaces exactly one parent-owned route and records its step.
// The existing group recovery repository preserves history, resets old observations and CASes versions.
func (backend *Backend) SwitchTarget(ctx context.Context, id uint, version uint64, targetID uint) error {
	return backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := ownedTask(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if detail.Task.State != d.Switching {
			return c.ErrStale
		}
		var route *d.BotRoute
		for index := range detail.Snapshot.BotRoutes {
			if detail.Snapshot.BotRoutes[index].TargetID == targetID {
				route = &detail.Snapshot.BotRoutes[index]
				break
			}
		}
		if route == nil {
			return c.ErrInvalid
		}
		input := groups.RecoveryInput{TaskID: id, TargetID: targetID, ExpectedVersion: route.Version, ExpectedRouteVersion: route.RouteVersion, OldBotID: detail.Snapshot.Old.BotID, NewBotID: detail.Snapshot.New.BotID, OldChatID: route.ChatID, NewChatID: route.DestinationChat()}
		store := routes.New(tx)
		if _, err := store.FreezeTarget(ctx, input); err != nil {
			return err
		}
		input.ExpectedVersion++
		if _, err := store.ReplaceRoute(ctx, input); err != nil {
			return err
		}
		return receipt(ctx, tasks.New(tx), id, fmt.Sprintf("bot.route.%d", targetID))
	})
}

// MoveModules atomically verifies every switched target, moves module intent and retires source config.
// It preserves old rows with incremented CAS, never copies session/message/media or contact data.
func (backend *Backend) MoveModules(ctx context.Context, id uint, version uint64) (*d.Task, error) {
	var result *d.Task
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := ownedTask(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if detail.Task.State != d.Switching {
			return c.ErrStale
		}
		for _, route := range detail.Snapshot.BotRoutes {
			target, err := routes.New(tx).Get(ctx, route.TargetID)
			if err != nil {
				return err
			}
			if target.RecoveryTaskID != id || target.BotInstanceID != detail.Snapshot.New.BotID || target.TelegramChatID != route.DestinationChat() || target.Version != route.Version+2 || target.RouteVersion != route.RouteVersion+1 {
				return c.ErrStale
			}
		}
		for _, module := range detail.Snapshot.BotModules {
			update := tx.Model(&gd.ModuleConfig{}).Where("bot_instance_id = ? AND partition = ? AND version = ?", detail.Snapshot.Old.BotID, module.Partition, module.Version).Updates(map[string]any{"version": module.Version + 1, "enabled": false, "target_id": 0})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return c.ErrStale
			}
			row := gd.ModuleConfig{BotInstanceID: detail.Snapshot.New.BotID, Partition: module.Partition, Version: 1, Enabled: module.Enabled, TargetID: module.TargetID, UpdatedBy: detail.Task.ConfirmedActorID}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		store := tasks.New(tx)
		if err := receipt(ctx, store, id, "bot.modules_moved"); err != nil {
			return err
		}
		result, err = store.Transition(ctx, c.Transition{TaskID: id, ExpectedVersion: version, State: d.Recovering})
		return err
	})
	return result, err
}

// Observe saves real permission evidence with all fences still held; late old-route responses fail CAS.
func (backend *Backend) Observe(ctx context.Context, id uint, version uint64, input groups.RecoveryObservation) error {
	return backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := ownedTask(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if detail.Task.State != d.Recovering || input.TaskID != id || input.BotID != detail.Snapshot.New.BotID || input.TelegramID != detail.Snapshot.New.TelegramID {
			return c.ErrStale
		}
		if err := checkModules(tx, detail.Snapshot, true); err != nil {
			return err
		}
		if err := routes.New(tx).ObserveRecovery(ctx, input); err != nil {
			return err
		}
		return receipt(ctx, tasks.New(tx), id, fmt.Sprintf("bot.permissions.%d", input.TargetID))
	})
}

// RecordModuleProof records only positive, module-specific receipts; remaining/unknown never succeeds.
func (backend *Backend) RecordModuleProof(ctx context.Context, id uint, version uint64, proof app.ModuleProof) error {
	if (proof.Module != "membership" && proof.Module != "support") || proof.Receipt == "" || len(proof.Receipt) > 128 || !proof.LocalComplete || proof.Remaining != 0 || proof.Unknown != 0 {
		return c.ErrInvalid
	}
	return backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := ownedTask(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if detail.Task.State != d.Recovering {
			return c.ErrStale
		}
		key := "bot.module." + proof.Module
		store := tasks.New(tx)
		step, err := store.EnsureStep(ctx, c.StepInput{TaskID: id, BusinessKey: key, Operation: key})
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(proof.Receipt))
		code := hex.EncodeToString(digest[:])
		if step.State == d.StepSucceeded {
			if step.ResultCode != code {
				return c.ErrStale
			}
			return nil
		}
		step, err = store.ClaimStep(ctx, c.StepClaim{StepID: step.ID, ExpectedVersion: step.Version, Lease: time.Minute})
		if err != nil {
			return err
		}
		_, err = store.CompleteStep(ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepSucceeded, ResultCode: code})
		return err
	})
}

// Finalize requires complete module steps and fresh per-target permission proofs in one transaction.
// Only the destination fence is released, with Enabled still false; old Bot stays permanently fenced.
func (backend *Backend) Finalize(ctx context.Context, id uint, version uint64) (*d.Task, error) {
	var result *d.Task
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, err := ownedTask(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if detail.Task.State != d.Recovering {
			return c.ErrStale
		}
		if err := checkModules(tx, detail.Snapshot, true); err != nil {
			return err
		}
		store := tasks.New(tx)
		for _, module := range []string{"membership", "support"} {
			var proof d.Step
			if err := tx.Where("task_id = ? AND business_key = ? AND state = ?", id, "bot.module."+module, d.StepSucceeded).Take(&proof).Error; err != nil {
				return c.ErrStale
			}
		}
		for _, route := range detail.Snapshot.BotRoutes {
			var proof d.Step
			if err := tx.Where("task_id = ? AND business_key = ? AND state = ?", id, "bot.module."+route.Purpose, d.StepSucceeded).Take(&proof).Error; err != nil {
				return c.ErrStale
			}
			target, err := routes.New(tx).Get(ctx, route.TargetID)
			if err != nil {
				return err
			}
			_, err = routes.New(tx).ReleaseRecovery(ctx, groups.RecoveryRelease{TaskID: id, TargetID: route.TargetID, ExpectedVersion: route.Version + 2, ExpectedRouteVersion: route.RouteVersion + 1, ExpectedObservationSeq: target.ObservationSeq, BotID: detail.Snapshot.New.BotID, ChatID: route.DestinationChat(), Generation: detail.Snapshot.ExpectedNew.Generation, BotTelegramID: detail.Snapshot.New.TelegramID})
			if err != nil {
				return err
			}
		}
		if err := bots.New(tx).ReleaseRecovery(ctx, fences(id, detail.Snapshot)[1]); err != nil {
			return err
		}
		if err := receipt(ctx, store, id, "bot.destination_released_disabled"); err != nil {
			return err
		}
		result, err = store.Transition(ctx, c.Transition{TaskID: id, ExpectedVersion: version, State: d.Completed})
		return err
	})
	return result, err
}
