package gormstore

import (
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
	"time"
)

// projectRefundExecution reads only this item's durable audit or strictly proven legacy enqueue.
// handled is true for revoked purchases even when evidence is absent: no latest-user fallback.
func projectRefundExecution(tx *gorm.DB, source d.LedgerSource, row *c.AdminOrderGroupState, lostAt time.Time) (bool, error) {
	if row.Rights != "revoked" {
		return false, nil
	}
	row.MemberRemoval, row.InviteCleanup = "unconfirmed", "unconfirmed"
	var audits []d.RefundDisposition
	if err := tx.Where("order_item_id = ? AND beneficiary_telegram_id = ? AND account_id = ?", source.OrderItemID, source.BeneficiaryTelegramID, source.AccountID).Find(&audits).Error; err != nil {
		return true, err
	}
	if len(audits) != 1 {
		return true, nil
	}
	audit := audits[0]
	if !audit.CreatedAt.Equal(lostAt) {
		return true, nil
	}
	selected := false
	for _, sourceID := range audit.SourceIDs {
		selected = selected || sourceID == source.ID
	}
	if !selected {
		return true, nil
	}
	executions := audit.Executions
	if audit.ExecutionVersion == 0 {
		var err error
		executions, err = legacyRefundExecutions(tx, source, audit)
		if err != nil {
			return true, err
		}
	} else if audit.ExecutionVersion != 1 {
		return true, nil
	}
	memberStates, cleanupStates := []string{}, []string{}
	for _, execution := range executions {
		if execution.TargetID != source.TargetID {
			continue
		}
		// Explicit mapped identities were captured by the writer; legacy reads only the original identity.
		if execution.TelegramID <= 0 {
			return true, c.ErrConflict
		}
		members, err := refundMemberStates(tx, execution, audit.CreatedAt)
		if err != nil {
			return true, err
		}
		cleanup, err := refundCleanupState(tx, execution)
		if err != nil {
			return true, err
		}
		memberStates = append(memberStates, members...)
		cleanupStates = append(cleanupStates, cleanup)
	}
	row.MemberRemoval = aggregateOrderEvidence(memberStates)
	row.MemberRemoved = row.MemberRemoval == "removed"
	row.InviteCleanup = aggregateOrderEvidence(cleanupStates)
	return true, nil
}

// legacyRefundExecutions accepts only the original atomic enqueue clock and unique revoke version.
// No rows are written: ambiguous/missing/cross-order or later scanner evidence stays unconfirmed.
func legacyRefundExecutions(tx *gorm.DB, source d.LedgerSource, audit d.RefundDisposition) ([]d.RefundExecution, error) {
	var outbox d.LedgerOutbox
	err := tx.Where("order_item_id = ? AND target_id = ? AND beneficiary_telegram_id = ? AND operation = ?", source.OrderItemID, source.TargetID, source.BeneficiaryTelegramID, "revoke").First(&outbox).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if outbox.ProjectionVersion == 0 || outbox.CreatedAt.Before(audit.CreatedAt) {
		return nil, nil
	}
	var competing int64
	if err := tx.Model(&d.LedgerOutbox{}).Where("target_id = ? AND beneficiary_telegram_id = ? AND projection_version = ? AND operation = ?", source.TargetID, source.BeneficiaryTelegramID, outbox.ProjectionVersion, "revoke").Count(&competing).Error; err != nil {
		return nil, err
	}
	if competing != 1 {
		return nil, nil
	}
	execution := d.RefundExecution{TargetID: source.TargetID, TelegramID: source.BeneficiaryTelegramID, EntitlementVersion: outbox.ProjectionVersion}
	var cycles []d.InviteCleanupCycle
	if err := tx.Where("target_id = ? AND telegram_id = ? AND entitlement_version = ? AND created_at = ?", source.TargetID, source.BeneficiaryTelegramID, outbox.ProjectionVersion, audit.CreatedAt).Find(&cycles).Error; err != nil {
		return nil, err
	}
	if len(cycles) > 1 {
		return nil, nil
	}
	if len(cycles) == 1 {
		execution.CleanupCycleID = cycles[0].ID
	}
	var tasks []d.AccessOperation
	if err := tx.Where("kind = ? AND target_id = ? AND telegram_id = ? AND entitlement_version = ? AND created_at = ?", "lifecycle", source.TargetID, source.BeneficiaryTelegramID, outbox.ProjectionVersion, audit.CreatedAt).Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.CleanupCycleID != execution.CleanupCycleID {
			return nil, nil
		}
		var member d.AccessMember
		err := tx.Where("target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", task.TargetID, task.TelegramID, task.BotID, task.ChatID, task.RouteVersion).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var owner d.LedgerSource
		err = tx.First(&owner, member.SourceID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if owner.TargetID != source.TargetID || owner.BeneficiaryTelegramID != source.BeneficiaryTelegramID || owner.AccountID != source.AccountID || owner.CreatedAt.After(audit.CreatedAt) {
			return nil, nil
		}
		execution.Members = append(execution.Members, d.RefundMemberExecution{BotID: task.BotID, ChatID: task.ChatID, RouteVersion: task.RouteVersion, OperationID: task.ID})
	}
	if len(cycles) == 0 && len(tasks) == 0 {
		return nil, nil
	}
	return []d.RefundExecution{execution}, nil
}

// refundMemberStates projects exact task results independently of cleanup and subsequent no-op tasks.
// Later positive observations on the same route can invalidate the removed display, never create it.
func refundMemberStates(tx *gorm.DB, execution d.RefundExecution, at time.Time) ([]string, error) {
	if execution.Covered {
		return []string{"not_required"}, nil
	}
	states := []string{}
	for _, binding := range execution.Members {
		state := "unconfirmed"
		var operation d.AccessOperation
		err := tx.First(&operation, binding.OperationID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		valid := binding.OperationID != 0 && err == nil && operation.Kind == "lifecycle" && operation.TargetID == execution.TargetID && operation.TelegramID == execution.TelegramID && operation.BotID == binding.BotID && operation.ChatID == binding.ChatID && operation.RouteVersion == binding.RouteVersion
		if valid {
			state = orderRemovalOutcome(operation)
		}
		var present int64
		observedAfter := at
		if valid && operation.UpdatedAt.After(observedAfter) {
			observedAfter = operation.UpdatedAt
		}
		if err := tx.Model(&d.AccessMember{}).Where("target_id = ? AND telegram_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ? AND is_member = ? AND observed_at > ?", execution.TargetID, execution.TelegramID, binding.BotID, binding.ChatID, binding.RouteVersion, true, observedAfter).Count(&present).Error; err != nil {
			return nil, err
		}
		if present > 0 {
			state = "present"
		}
		states = append(states, state)
	}
	if len(states) == 0 {
		states = append(states, "unconfirmed")
	}
	return states, nil
}

// refundCleanupState reads this execution's cycle, including invitations from older purchases.
// Each item and optional revoke operation must match its original physical route and identity.
func refundCleanupState(tx *gorm.DB, execution d.RefundExecution) (string, error) {
	if execution.CleanupCycleID == 0 {
		if execution.Covered {
			return "not_required", nil
		}
		return "none", nil
	}
	var cycle d.InviteCleanupCycle
	err := tx.First(&cycle, execution.CleanupCycleID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "unconfirmed", nil
	}
	if err != nil {
		return "", err
	}
	if cycle.TargetID != execution.TargetID || cycle.TelegramID != execution.TelegramID || cycle.EntitlementVersion != execution.EntitlementVersion {
		return "unconfirmed", nil
	}
	var items []d.InviteCleanupItem
	if err := tx.Where("cycle_id = ?", cycle.ID).Find(&items).Error; err != nil {
		return "", err
	}
	states := []string{}
	for _, item := range items {
		var invite d.AccessInvite
		err := tx.First(&invite, item.InviteID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
		if err != nil || invite.TargetID != execution.TargetID || invite.TelegramID != execution.TelegramID {
			states = append(states, "unconfirmed")
			continue
		}
		outcome := item.State
		if item.OperationID != 0 {
			var operation d.AccessOperation
			err := tx.First(&operation, item.OperationID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", err
			}
			if err != nil || operation.Kind != "revoke" || operation.InviteID != invite.ID || operation.TargetID != invite.TargetID || operation.TelegramID != invite.TelegramID || operation.BotID != invite.BotID || operation.ChatID != invite.ChatID || operation.RouteVersion != invite.RouteVersion {
				outcome = "unconfirmed"
			} else if outcome != "succeeded" {
				outcome = operation.State
			}
		}
		states = append(states, refundCleanupOutcome(outcome))
	}
	return aggregateOrderEvidence(states), nil
}

// refundCleanupOutcome normalizes persisted cleanup outcomes without assuming external success.
// outcome is an item/operation state; unknown states remain explicitly unconfirmed.
func refundCleanupOutcome(outcome string) string {
	switch outcome {
	case "succeeded":
		return "succeeded"
	case "failed", "needs_review":
		return "failed"
	case "unknown":
		return "unknown"
	case "pending", "running", "creating", "retryable":
		return "pending"
	default:
		return "unconfirmed"
	}
}
