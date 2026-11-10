package routerecovery

import (
	"context"
	"fmt"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// scanReceipts retains late/unknown result liabilities independently of the operation state.
// A failed local operation never erases an earlier successful or uncertain external attempt.
func scanReceipts(tx *gorm.DB, request scanRequest, at time.Time) error {
	var receipts []d.AccessResult
	operations := accessScope(tx.Model(&d.AccessOperation{}), request).Select("id")
	if err := tx.Where("operation_id IN (?)", operations).Find(&receipts).Error; err != nil {
		return err
	}
	for _, receipt := range receipts {
		var operation d.AccessOperation
		if err := tx.First(&operation, receipt.OperationID).Error; err != nil {
			return err
		}
		state := "resolved"
		// Management recover writes local audit receipts, not Telegram results.
		// Recognize only the complete writer signature; arbitrary accepted/blocked
		// outcomes remain unknown and cannot discharge an external attempt.
		if localRecoveryAudit(receipt) {
			key := fmt.Sprintf("%d:%d", receipt.OperationID, receipt.LeaseVersion)
			if err := record(tx, request, at, Evidence{Kind: "receipt", Reference: key, State: state}); err != nil {
				return err
			}
			continue
		}
		// Lifecycle check/verify receipts are local state-machine transitions, not
		// external uncertain effects. Preserve actual unknown ban/unban receipts.
		localLifecycle := operation.Kind == "lifecycle" && (receipt.Action != "ban" && receipt.Action != "unban" || receipt.Action == "ban" && receipt.Reason == "administrator_member" && operation.BanOutcome == "" && !operation.BanConfirmed) && (receipt.Outcome == "pending" || receipt.Outcome == "needs_review")
		if localLifecycle {
			if operation.State != "succeeded" {
				state = "remaining"
			}
		} else if receipt.Outcome == "unknown" || (receipt.Outcome == "succeeded" && operation.State != "succeeded") {
			state = "unknown"
		} else if receipt.Outcome != "succeeded" && receipt.Outcome != "failed" && receipt.Outcome != "retryable" {
			state = "unknown"
		}
		key := fmt.Sprintf("%d:%d", receipt.OperationID, receipt.LeaseVersion)
		if err := record(tx, request, at, Evidence{Kind: "receipt", Reference: key, State: state}); err != nil {
			return err
		}
	}
	return nil
}

// CheckGroupResume inspects module evidence inside bootstrap's parent/step transaction.
// tx must be the coordinator transaction; input is derived from its locked snapshot.
// It never invokes HTTP, clears unknown effects, changes operations or grants completion.
func (worker *GroupRecovery) CheckGroupResume(ctx context.Context, tx *gorm.DB, input Request) error {
	if tx == nil || input.ActorID == 0 || input.Purpose != "membership" {
		return c.ErrInvalid
	}
	session, err := worker.session(ctx, input)
	if err != nil {
		return err
	}
	if err := session.fence(tx); err != nil {
		return err
	}
	if _, err := lockRun(tx, input); err != nil {
		return err
	}
	scope := scanRequest{Request: input, OldBotID: input.BotID, NewBotID: input.BotID}
	safe, err := resumeOperations(tx, scope)
	if err != nil {
		return err
	}
	if err := scanHistorical(tx, scope); err != nil {
		return err
	}
	if err := session.scanAdmission(tx, scope); err != nil {
		return err
	}
	if err := session.bindOperations(tx, scope, time.Now().UTC()); err != nil {
		return err
	}
	var evidence []Evidence
	if err := tx.Where("task_id = ? AND target_id = ? AND state = ?", input.TaskID, input.TargetID, "unknown").Find(&evidence).Error; err != nil {
		return err
	}
	for _, row := range evidence {
		// Only task-owned definite no-effect lifecycle wrappers may be rechecked.
		// Independent ban/unban receipts still block, even when the wrapper failed.
		reference := row.Reference
		if row.Kind == "recovery_action" {
			reference = "action:" + reference
		}
		if (row.Kind != "operation" && row.Kind != "recovery_action") || !safe[reference] {
			return c.ErrConflict
		}
	}
	return nil
}

// resumeOperations rejects live leases and restricts lifecycle exceptions to this
// task's definite failed ban/no-write admin protection with matching operation binding.
func resumeOperations(tx *gorm.DB, scope scanRequest) (map[string]bool, error) {
	var rows []d.AccessOperation
	if err := accessScope(tx, scope).Find(&rows).Error; err != nil {
		return nil, err
	}
	var actions []Action
	if err := tx.Where("task_id = ? AND target_id = ?", scope.TaskID, scope.TargetID).Find(&actions).Error; err != nil {
		return nil, err
	}
	for _, action := range actions {
		if action.State != "pending" && action.State != "retryable" && action.State != "failed" && action.State != "succeeded" {
			return nil, c.ErrConflict
		}
	}
	safe := map[string]bool{}
	for _, row := range rows {
		if row.BanOutcome == c.AccessUnknown || row.UnbanOutcome == c.AccessUnknown || row.LeaseToken != "" || row.State == "creating" {
			return nil, c.ErrConflict
		}
		if !safeCleanupRecheck(row) {
			continue
		}
		if row.Kind != "lifecycle" || row.BusinessKey != fmt.Sprintf("route-recovery:%d:%d:%d", scope.TaskID, scope.TargetID, row.TelegramID) || row.TargetID != scope.TargetID || row.BotID != scope.BotID || row.ChatID != scope.OldChatID || row.RouteVersion != scope.OldRouteVersion || row.Generation != scope.Generation {
			return nil, c.ErrConflict
		}
		var count int64
		reference := fmt.Sprint(row.ID)
		if err := tx.Model(&Action{}).Where("task_id = ? AND target_id = ? AND kind = ? AND reference = ? AND state = ?", scope.TaskID, scope.TargetID, "group_operation", reference, "succeeded").Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, c.ErrConflict
		}
		safe[reference], safe["action:group_operation:"+reference] = true, true
	}
	return safe, nil
}

// localRecoveryAudit identifies receipts emitted by the existing RecoverAccess writer.
// receipt must retain actor, fixed reason and allowlisted action; true describes only
// local intent and never changes the operation or any earlier external receipt.
func localRecoveryAudit(receipt d.AccessResult) bool {
	if receipt.ActorAdminID != 0 && receipt.Reason == "admin_recovery" && receipt.Outcome == "reviewed" {
		return receipt.Action == "recheck" || receipt.Action == "retry" || receipt.Action == "block_ban"
	}
	if receipt.ActorAdminID == 0 || receipt.Reason != "operator_verified" || (receipt.Outcome != "accepted" && receipt.Outcome != "blocked") {
		return false
	}
	switch receipt.Action {
	case "retry", "reconcile", "refresh_member", "rebuild", "review":
		return true
	default:
		return false
	}
}
