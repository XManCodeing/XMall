// Package telegramidentityrecovery connects recovery tasks to existing membership mappings.
// All mapping writes and receipts share one connection; this adapter never calls Telegram.
package telegramidentityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	m "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	md "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	memberstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	mappings "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Backend composes the existing mapping writer with durable task/step receipts.
type Backend struct{ db *gorm.DB }

// New returns a database-only adapter; caller owns migrations and authorization.
func New(db *gorm.DB) *Backend { return &Backend{db: db} }

// PreviewIdentityMigration delegates unchanged to the established membership preflight.
func (backend *Backend) PreviewIdentityMigration(ctx context.Context, input m.IdentityMigrationInput, at time.Time) (*m.IdentityMigrationPreview, error) {
	store, err := mappings.New(backend.db)
	if err != nil {
		return nil, err
	}
	return store.PreviewIdentityMigration(ctx, input, at)
}

// Commit atomically persists mappings, a successful commit step, and recovering state.
// A process crash cannot leave a mapping without its receipt. Nested stores only receive tx.
func (backend *Backend) Commit(ctx context.Context, detail *c.Detail) error {
	if detail == nil || detail.Task.ID == 0 {
		return c.ErrInvalid
	}
	err := dblock.Transaction(ctx, backend.db, func(tx *gorm.DB) error {
		store := tasks.New(tx)
		current, err := lockIdentityTask(tx, detail.Task.ID)
		if err != nil {
			return err
		}
		if current.Task.Kind != d.IdentityTransfer || current.Task.ActorID != detail.Task.ActorID || current.Task.PreflightRef != detail.Task.PreflightRef {
			return c.ErrStale
		}
		if current.Task.State == d.Recovering || current.Task.State == d.Completed {
			return nil
		}
		if current.Task.State != d.Fencing || current.Task.Version != detail.Task.Version {
			return c.ErrStale
		}
		membership, err := memberstore.InTransaction(tx)
		if err != nil {
			return err
		}
		ready, err := membership.PrepareIdentityInvites(ctx, a.IdentityCommitInput(current), time.Now().UTC())
		if err != nil {
			return err
		}
		if !ready {
			// Commit the durable retirement authorization before runtime dispatch.
			// A successful local checkpoint is not a claimed Telegram revocation.
			if err := receipt(ctx, store, current.Task.ID, "identity_invites_retirement_requested"); err != nil {
				return err
			}
			return nil
		}
		if ready {
			// A no-invitation migration needs no synthetic cleanup receipt.
			var requested int64
			if err := tx.Model(&d.Step{}).Where("task_id = ? AND business_key = ?", current.Task.ID, "identity_invites_retirement_requested").Count(&requested).Error; err != nil {
				return err
			}
			if requested > 0 {
				if err := receipt(ctx, store, current.Task.ID, "identity_invites_cleanup_verified"); err != nil {
					return err
				}
			}
		}
		task, err := store.Transition(ctx, c.Transition{TaskID: current.Task.ID, ExpectedVersion: current.Task.Version, State: d.Switching})
		if err != nil {
			return err
		}
		writer, err := mappings.New(tx)
		if err != nil {
			return err
		}
		if err := writer.CommitIdentityMigration(ctx, a.IdentityCommitInput(current), time.Now().UTC()); err != nil {
			return err
		}
		if err := receipt(ctx, store, task.ID, "identity_mapping_committed"); err != nil {
			return err
		}
		_, err = store.Transition(ctx, c.Transition{TaskID: task.ID, ExpectedVersion: task.Version, State: d.Recovering})
		return err
	})
	slog.InfoContext(ctx, "identity recovery commit checkpoint", "task_id", detail.Task.ID, "committed", err == nil)
	return err
}

// receipt records a local atomic operation; no lease can escape the surrounding transaction.
// key is an internal fixed classification, never platform text or administrator input.
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
	_, err = store.CompleteStep(ctx, c.StepCompletion{StepID: step.ID, ExpectedVersion: step.Version, LeaseToken: step.LeaseToken, Outcome: d.StepSucceeded, ResultCode: key})
	return err
}

// Completed verifies every exact mapping's admission and cleanup receipts, then releases scope.
// Pending mappings remain recovering; a missing/mismatched mapping fails closed.
func (backend *Backend) Completed(ctx context.Context, detail *c.Detail) (bool, error) {
	complete := false
	err := backend.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := tasks.New(tx)
		current, err := store.Get(ctx, detail.Task.ID)
		if err != nil {
			return err
		}
		if current.Task.Kind != d.IdentityTransfer || current.Task.ActorID != detail.Task.ActorID || current.Task.PreflightRef != detail.Task.PreflightRef {
			return c.ErrStale
		}
		if current.Task.State == d.Completed {
			complete = true
			return nil
		}
		if current.Task.State == d.Fencing {
			// Invitation retirement is durable but still awaiting runtime evidence.
			return nil
		}
		if current.Task.State != d.Recovering {
			return c.ErrStale
		}
		ready, err := mappingProof(tx, current)
		if err != nil || !ready {
			return err
		}
		if err := receipt(ctx, store, current.Task.ID, "identity_cleanup_verified"); err != nil {
			return err
		}
		_, err = store.Transition(ctx, c.Transition{TaskID: current.Task.ID, ExpectedVersion: current.Task.Version, State: d.Completed})
		complete = err == nil
		return err
	})
	return complete, err
}

// mappingProof checks only existing runtime/lifecycle facts and the exact reviewed route.
// Historical orders, sources, projections and expiry values are never rewritten here.
func mappingProof(tx *gorm.DB, detail *c.Detail) (bool, error) {
	for _, target := range detail.Snapshot.IdentityTargets {
		var row md.IdentityMigration
		err := tx.Where("target_id = ? AND account_id = ? AND old_telegram_id = ? AND new_telegram_id = ?", target.TargetID, detail.Snapshot.Old.AccountID, detail.Snapshot.Old.TelegramID, detail.Snapshot.New.TelegramID).Take(&row).Error
		if err != nil {
			return false, err
		}
		if row.ActorAdminID != detail.Task.ActorID || strconv.FormatUint(uint64(row.BotID), 10) != target.BotID || strconv.FormatInt(row.ChatID, 10) != target.ChatID || strconv.FormatUint(row.RouteVersion, 10) != target.RouteVersion || strconv.FormatUint(row.GroupVersion, 10) != target.GroupVersion || strconv.FormatUint(row.EntitlementVersion, 10) != target.EntitlementVersion || uint64(row.BindingVersion) != detail.Snapshot.ExpectedOld.Binding {
			return false, c.ErrStale
		}
		if row.State != "completed" || row.AdmittedAt == nil || row.CompletedAt == nil {
			return false, nil
		}
	}
	return len(detail.Snapshot.IdentityTargets) > 0, nil
}

// lockIdentityTask locks id before any proof query using tx and returns the stored snapshot.
// Commit and cancellation share this parent lock; missing/malformed records fail closed.
func lockIdentityTask(tx *gorm.DB, id uint) (*c.Detail, error) {
	if err := dblock.Row(tx.Model(&d.Task{}).Where("id = ?", id), "version", c.ErrNotFound); err != nil {
		return nil, err
	}
	var detail c.Detail
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&detail.Task, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, c.ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(detail.Task.SnapshotJSON), &detail.Snapshot); err != nil {
		return nil, err
	}
	return &detail, nil
}

// CancelConfirmed atomically cancels input's confirmed identity task before mapping commit.
// Cleanup authorization is irreversible: cancellation never restores an old invitation.
// It never checks live route/binding versions: drift is precisely why a fresh review is needed.
// Successful replays return the original cancellation without deleting any newly owned scope.
func (backend *Backend) CancelConfirmed(ctx context.Context, input c.Cancellation) (*d.Task, error) {
	if backend == nil || backend.db == nil || input.TaskID == 0 || input.ActorID == 0 || input.ExpectedVersion == 0 {
		return nil, c.ErrInvalid
	}
	var task *d.Task
	err := dblock.Transaction(ctx, backend.db, func(tx *gorm.DB) error {
		detail, err := lockIdentityTask(tx, input.TaskID)
		if err != nil {
			return err
		}
		task = &detail.Task
		if task.Kind != d.IdentityTransfer || task.ActorID != input.ActorID || task.ConfirmedActorID != input.ActorID || task.ConfirmedAt == nil || task.ConfirmedVersion == 0 || task.VerificationNote == "" || len(task.PreflightRef) != 64 {
			return c.ErrStale
		}
		if err := cancellationSnapshot(detail.Snapshot); err != nil {
			return err
		}
		if task.State == d.Cancelled && task.CancelledActorID == input.ActorID && task.CancelledVersion == input.ExpectedVersion && task.Version == input.ExpectedVersion+1 {
			return nil
		}
		if task.State != d.Fencing || task.Version != input.ExpectedVersion || task.ConfirmedVersion+1 != task.Version {
			return c.ErrStale
		}
		if err := cancellationUntouched(tx, detail); err != nil {
			return err
		}
		now := time.Now().UTC()
		result := tx.Model(&d.Task{}).Where("id = ? AND version = ? AND state = ?", task.ID, input.ExpectedVersion, d.Fencing).
			Updates(map[string]any{"state": d.Cancelled, "version": task.Version + 1, "cancelled_version": task.Version, "cancelled_actor_id": input.ActorID, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return c.ErrStale
		}
		if err := tx.Where("task_id = ?", task.ID).Delete(&d.ScopeSlot{}).Error; err != nil {
			return err
		}
		task.CancelledVersion, task.CancelledActorID = task.Version, input.ActorID
		task.State, task.Version, task.UpdatedAt = d.Cancelled, task.Version+1, now
		return nil
	})
	slog.InfoContext(ctx, "identity recovery cancellation checkpoint", "task_id", input.TaskID, "cancelled", err == nil)
	if err != nil {
		return nil, err
	}
	return task, nil
}

// cancellationSnapshot validates stored endpoints, exact target coverage and positive evidence versions.
// snapshot is immutable review evidence; validity does not require current live evidence to match.
func cancellationSnapshot(snapshot d.Snapshot) error {
	if snapshot.Old.AccountID == 0 || snapshot.Old.AccountID != snapshot.New.AccountID || snapshot.Old.TelegramID <= 0 || snapshot.New.TelegramID <= 0 || snapshot.Old.TelegramID == snapshot.New.TelegramID || snapshot.ExpectedOld.Binding == 0 || len(snapshot.IdentityTargets) == 0 || len(snapshot.IdentityTargets) > 100 {
		return c.ErrStale
	}
	if !slices.Contains(snapshot.Scope.AccountIDs, strconv.FormatUint(uint64(snapshot.Old.AccountID), 10)) || !slices.Contains(snapshot.Scope.TelegramIDs, strconv.FormatInt(snapshot.Old.TelegramID, 10)) || !slices.Contains(snapshot.Scope.TelegramIDs, strconv.FormatInt(snapshot.New.TelegramID, 10)) {
		return c.ErrStale
	}
	targets := make(map[string]bool, len(snapshot.IdentityTargets))
	for _, target := range snapshot.IdentityTargets {
		if targets[target.TargetID] || !slices.Contains(snapshot.Scope.BotIDs, target.BotID) || !slices.Contains(snapshot.Scope.ChatIDs, target.ChatID) {
			return c.ErrStale
		}
		for _, value := range []string{target.TargetID, target.BotID, target.EntitlementVersion, target.GroupVersion, target.RouteVersion} {
			number, err := strconv.ParseUint(value, 10, 64)
			if err != nil || number == 0 || strconv.FormatUint(number, 10) != value {
				return c.ErrStale
			}
		}
		chat, err := strconv.ParseInt(target.ChatID, 10, 64)
		if err != nil || chat == 0 || strconv.FormatInt(chat, 10) != target.ChatID {
			return c.ErrStale
		}
		targets[target.TargetID] = true
	}
	if len(snapshot.Scope.TargetIDs) != len(targets) {
		return c.ErrStale
	}
	for _, target := range snapshot.Scope.TargetIDs {
		if !targets[target] {
			return c.ErrStale
		}
		delete(targets, target)
	}
	return nil
}

// cancellationUntouched permits only completed local invite-cleanup checkpoints before mapping creation.
// Cleanup operations, including unknown effects, remain durable and cannot revive old links.
// tx already owns the parent lock; no account/state filter may hide historical overlapping mappings.
func cancellationUntouched(tx *gorm.DB, detail *c.Detail) error {
	var count int64
	allowed := []string{"identity_invites_retirement_requested", "identity_invites_cleanup_verified"}
	if err := tx.Model(&d.Step{}).Where("task_id = ? AND (business_key NOT IN ? OR operation NOT IN ? OR state <> ?)", detail.Task.ID, allowed, allowed, d.StepSucceeded).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return c.ErrStale
	}
	identities := []int64{detail.Snapshot.Old.TelegramID, detail.Snapshot.New.TelegramID}
	for _, target := range detail.Snapshot.IdentityTargets {
		if err := tx.Model(&md.IdentityMigration{}).Where("target_id = ? AND (old_telegram_id IN ? OR new_telegram_id IN ?)", target.TargetID, identities, identities).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return c.ErrStale
		}
	}
	return nil
}

var _ a.IdentityBackend = (*Backend)(nil)
