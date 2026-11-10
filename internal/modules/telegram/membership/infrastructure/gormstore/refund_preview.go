package gormstore

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"gorm.io/gorm"
)

// unfinishedLifecycleQuery scopes one physical member's outstanding tasks on tx.
// Session isolates each Count/Update/First finisher, preventing SELECT/FROM leakage.
func unfinishedLifecycleQuery(tx *gorm.DB, member d.AccessMember) *gorm.DB {
	return tx.Model(&d.AccessOperation{}).Where("kind = ? AND target_id = ? AND bot_id = ? AND chat_id = ? AND telegram_id = ? AND state <> ?", "lifecycle", member.TargetID, member.BotID, member.ChatID, member.TelegramID, "succeeded").Session(&gorm.Session{})
}

// captureLifecycleID returns the exact enqueued/reused business-key task to optional callers.
// tx is the enqueue transaction; no later or successful unrelated task is selected.
func captureLifecycleID(tx *gorm.DB, operation d.AccessOperation, outputs ...*uint) error {
	if len(outputs) == 0 {
		return nil
	}
	var persisted d.AccessOperation
	if err := tx.Where("business_key = ?", operation.BusinessKey).First(&persisted).Error; err != nil {
		return err
	}
	if persisted.TargetID != operation.TargetID || persisted.TelegramID != operation.TelegramID || persisted.BotID != operation.BotID || persisted.ChatID != operation.ChatID || persisted.RouteVersion != operation.RouteVersion {
		return c.ErrConflict
	}
	for _, output := range outputs {
		if output != nil {
			*output = persisted.ID
		}
	}
	return nil
}

// enqueueRefundExecutions schedules one revoked target in the caller's atomic disposition.
// It returns immutable physical identity/route bindings; original invitation sources stay intact.
func enqueueRefundExecutions(tx *gorm.DB, target uint, beneficiary int64, at time.Time) ([]d.RefundExecution, error) {
	identities := []int64{beneficiary}
	mapping, err := im.Mapping(tx, beneficiary, target)
	if err != nil {
		return nil, err
	}
	if mapping != nil {
		identities = []int64{mapping.OldTelegramID, mapping.NewTelegramID}
	}
	executions := make([]d.RefundExecution, 0, len(identities))
	for _, identity := range identities {
		sources, _, _, err := im.Sources(tx, identity, target)
		if err != nil {
			return nil, err
		}
		if len(sources) == 0 {
			// A tombstone may precede the grant; record no execution, not fictitious success.
			executions = append(executions, d.RefundExecution{TargetID: target, TelegramID: identity})
			continue
		}
		eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: identity}, target, at)
		if err != nil {
			return nil, err
		}
		cycle, err := enqueueInviteCleanup(tx, target, identity, at)
		if err != nil {
			return nil, err
		}
		execution := d.RefundExecution{TargetID: target, TelegramID: identity, EntitlementVersion: eligible.Version, CleanupCycleID: cycle, Covered: eligible.Active}
		var members []d.AccessMember
		if err := tx.Where("target_id = ? AND telegram_id = ?", target, identity).Find(&members).Error; err != nil {
			return nil, err
		}
		for _, member := range members {
			binding := d.RefundMemberExecution{BotID: member.BotID, ChatID: member.ChatID, RouteVersion: member.RouteVersion}
			if err := enqueueLifecycle(tx, member, at, &binding.OperationID); err != nil {
				return nil, err
			}
			execution.Members = append(execution.Members, binding)
		}
		executions = append(executions, execution)
	}
	return executions, nil
}

// OrderStates reads all authorized snapshot targets without side effects or live product settings.
// itemIDs are preauthorized; at is the observation clock. All facts share one read transaction.
func (store *Store) OrderStates(ctx context.Context, itemIDs []uint, at time.Time) ([]c.AdminOrderGroupState, error) {
	rows := []c.AdminOrderGroupState{}
	if len(itemIDs) == 0 {
		return rows, nil
	}
	options := &sql.TxOptions{ReadOnly: true}
	if store.db.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshots []d.OrderItemSnapshot
		if err := tx.Where("order_item_id IN ?", itemIDs).Order("order_item_id").Find(&snapshots).Error; err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			for _, target := range snapshot.Targets {
				row := c.AdminOrderGroupState{ItemID: snapshot.OrderItemID, TargetID: target.TargetID, BeneficiaryTelegramID: strconv.FormatInt(snapshot.BeneficiaryTelegramID, 10), Rights: "unconfirmed", MemberRemoval: "unconfirmed", InviteCleanup: "unconfirmed"}
				if err := orderGroupRights(tx, snapshot, &row, at); err != nil {
					return err
				}
				if err := orderGroupOperations(tx, snapshot, &row, at); err != nil {
					return err
				}
				rows = append(rows, row)
			}
		}
		return nil
	}, options)
	return rows, err
}

// orderGroupRights computes this purchase's accrued segment using all sources and tombstones.
// Later renewals cannot revive expired purchases; missing evidence never implies active rights.
func orderGroupRights(tx *gorm.DB, snapshot d.OrderItemSnapshot, row *c.AdminOrderGroupState, at time.Time) error {
	sources, revoked, _, err := im.Sources(tx, snapshot.BeneficiaryTelegramID, row.TargetID)
	if err != nil {
		return err
	}
	scope := d.EntitlementScope{BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, TargetID: row.TargetID}
	inputs := []d.EntitlementSource{}
	tombstones := []d.SourceKey{}
	found, terminated := false, false
	for _, source := range sources {
		if source.OrderItemID == row.ItemID {
			found = true
		}
		inputs = append(inputs, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: source.OrderItemID}, PaidAt: source.PaidAt, Kind: source.Kind, Months: source.Months, State: d.SourceActive})
	}
	for _, tombstone := range revoked {
		if tombstone.OrderItemID == row.ItemID {
			terminated = true
		}
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: tombstone.OrderItemID})
	}
	if !found {
		row.Rights = "none"
		var paid int64
		if err := tx.Table("order_items AS item").Joins("JOIN orders leaf ON leaf.id = item.order_id").Where("item.id = ? AND item.deleted_at IS NULL AND leaf.deleted_at IS NULL AND leaf.paid_at IS NOT NULL AND leaf.status <> ?", row.ItemID, "canceled").Count(&paid).Error; err != nil {
			return err
		}
		if paid > 0 && !terminated {
			row.Rights = "pending"
		}
		return nil
	}
	if terminated {
		row.Rights = "revoked"
		return nil
	}
	calculation, err := d.RecalculateEntitlement(scope, inputs, tombstones, at)
	if err != nil {
		return err
	}
	prefix := []d.EntitlementSource{}
	for _, source := range calculation.Sources {
		prefix = append(prefix, source)
		if source.Key.OrderItemID == row.ItemID {
			if source.PaidAt.After(at) {
				return nil
			}
			row.Permanent = source.Kind == d.TermPermanent
			break
		}
	}
	purchase, err := d.RecalculateEntitlement(scope, prefix, tombstones, at)
	if err != nil {
		return err
	}
	row.ExpiresAt = purchase.LimitedExpiresAt
	if row.Permanent {
		row.ExpiresAt = nil
	}
	row.Rights = "expired"
	if row.Permanent || (row.ExpiresAt != nil && row.ExpiresAt.After(at)) {
		row.Rights = "active"
	}
	return nil
}

// orderGroupOperations only projects liabilities proven to belong to this purchase.
// Snapshot identity, source IDs and the purchase loss window prevent cross-order evidence.
func orderGroupOperations(tx *gorm.DB, snapshot d.OrderItemSnapshot, row *c.AdminOrderGroupState, at time.Time) error {
	row.MemberRemoval, row.InviteCleanup = "none", "none"
	if row.Rights != "expired" && row.Rights != "revoked" {
		return nil
	}
	return projectOrderLiabilities(tx, snapshot, row, at)
}

// aggregateOrderEvidence requires unanimous outcomes; mixed routes never become success.
// states retain unknown external outcomes separately from absent/unprocessed evidence.
func aggregateOrderEvidence(states []string) string {
	if len(states) == 0 {
		return "unconfirmed"
	}
	result := states[0]
	for _, state := range states {
		if state != result {
			return "mixed"
		}
	}
	return result
}

// MatchingOrderItems reuses the order projection before the order repository counts/pages.
// filter is validated; all dimensions match one item/target. No business work is scheduled.
func (store *Store) MatchingOrderItems(ctx context.Context, filter c.AdminOrderStateFilter, at time.Time) ([]uint, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	var itemIDs []uint
	if err := store.db.WithContext(ctx).Model(&d.OrderItemSnapshot{}).Order("order_item_id").Pluck("order_item_id", &itemIDs).Error; err != nil {
		return nil, err
	}
	rows, err := store.OrderStates(ctx, itemIDs, at)
	if err != nil {
		return nil, err
	}
	matches, seen := []uint{}, map[uint]bool{}
	for _, row := range rows {
		if filter.Matches(row) && !seen[row.ItemID] {
			matches = append(matches, row.ItemID)
			seen[row.ItemID] = true
		}
	}
	return matches, nil
}

// projectOrderLiabilities reads source-bound invitations and member evidence for one lost purchase.
// tx is the caller's read transaction; at is its observation clock. No cleanup is scheduled.
func projectOrderLiabilities(tx *gorm.DB, snapshot d.OrderItemSnapshot, row *c.AdminOrderGroupState, at time.Time) error {
	var source d.LedgerSource
	if err := tx.Where("order_item_id = ? AND target_id = ? AND operation = ?", row.ItemID, row.TargetID, "grant").First(&source).Error; err != nil {
		return err
	}
	lostAt := row.ExpiresAt
	if row.Rights == "revoked" {
		var revoked d.LedgerRevocation
		if err := tx.First(&revoked, "order_item_id = ? AND target_id = ?", row.ItemID, row.TargetID).Error; err != nil {
			return err
		}
		lostAt = &revoked.RevokedAt
	}
	// 回收与执行器一致，按全部权益判断；不能撤掉旧单贡献后再判断续费是否有效。
	coverage, coverageErr := previewRemaining(tx, snapshot.BeneficiaryTelegramID, row.TargetID, nil, at)
	if coverageErr != nil && !errors.Is(coverageErr, c.ErrConflict) {
		return coverageErr
	}
	if row.Rights == "revoked" && lostAt != nil {
		handled, err := projectRefundExecution(tx, source, row, *lostAt)
		if err != nil {
			return err
		}
		if handled {
			if coverageErr == nil && coverage.Active {
				row.MemberRemoval, row.MemberRemoved = "not_required", false
			}
			return nil
		}
	}
	if err := orderInviteLiabilities(tx, source, row, coverageErr == nil && coverage.Active); err != nil {
		return err
	}
	remaining, err := previewRemaining(tx, snapshot.BeneficiaryTelegramID, row.TargetID, map[uint]bool{row.ItemID: true}, at)
	if errors.Is(err, c.ErrConflict) {
		row.MemberRemoval = "unconfirmed"
		return nil
	}
	if err != nil {
		return err
	}
	if remaining.Active {
		row.MemberRemoval = "not_required"
		return nil
	}
	return orderMemberLiabilities(tx, source, lostAt, row)
}

// orderInviteLiabilities 汇总 source 对应邀请的回收证据到 row，返回数据库读取错误。
// covered 表示全部权益仍有效；仅无回收记录且邀请正常时显示无需处理，不覆盖已有结果。
func orderInviteLiabilities(tx *gorm.DB, source d.LedgerSource, row *c.AdminOrderGroupState, covered bool) error {
	var invites []d.AccessInvite
	if err := tx.Where("source_id = ? AND target_id = ? AND telegram_id = ?", source.ID, source.TargetID, source.BeneficiaryTelegramID).Find(&invites).Error; err != nil {
		return err
	}
	states := make([]string, 0, len(invites))
	for _, invite := range invites {
		var items []d.InviteCleanupItem
		if err := tx.Where("invite_id = ?", invite.ID).Order("cycle_id DESC").Find(&items).Error; err != nil {
			return err
		}
		state := "unconfirmed"
		if len(items) == 0 && covered && invite.State == "active" {
			// 独立撤销操作也可能尚未进入清理周期，不得将其异常或在途结果掩盖。
			var revokeCount int64
			if err := tx.Model(&d.AccessOperation{}).Where("invite_id = ? AND kind = ?", invite.ID, "revoke").Count(&revokeCount).Error; err != nil {
				return err
			}
			if revokeCount == 0 {
				state = "not_required"
			}
		}
		if len(items) > 0 {
			outcome := items[0].State
			if items[0].OperationID != 0 && outcome != "succeeded" {
				var operation d.AccessOperation
				if err := tx.First(&operation, items[0].OperationID).Error; err != nil {
					return err
				}
				// A corrupt association cannot borrow another invitation's successful revoke.
				if operation.InviteID == invite.ID && operation.Kind == "revoke" {
					outcome = operation.State
				}
			}
			switch outcome {
			case "succeeded":
				state = "succeeded"
			case "failed", "needs_review":
				state = "failed"
			case "unknown":
				state = "unknown"
			case "pending", "running", "creating", "retryable":
				state = "pending"
			}
		}
		states = append(states, state)
	}
	if len(states) > 0 {
		row.InviteCleanup = aggregateOrderEvidence(states)
	}
	return nil
}

// orderMemberLiabilities uses source-bound managed routes and a bounded purchase-loss window.
// Newer purchases close that window so account-wide operations cannot stand in for this order.
func orderMemberLiabilities(tx *gorm.DB, source d.LedgerSource, lostAt *time.Time, row *c.AdminOrderGroupState) error {
	row.MemberRemoval = "unconfirmed"
	if lostAt == nil || lostAt.IsZero() {
		return nil
	}
	var next []d.LedgerSource
	// 已在失效前撤销的来源不再覆盖本单，也不能截断本单的处置窗口。
	revoked := tx.Model(&d.LedgerRevocation{}).Select("order_item_id").Where("beneficiary_telegram_id = ? AND target_id = ? AND revoked_at <= ?", source.BeneficiaryTelegramID, source.TargetID, *lostAt)
	if err := tx.Where("beneficiary_telegram_id = ? AND target_id = ? AND order_item_id <> ? AND paid_at > ? AND order_item_id NOT IN (?)", source.BeneficiaryTelegramID, source.TargetID, source.OrderItemID, source.PaidAt, revoked).Order("paid_at").Limit(1).Find(&next).Error; err != nil {
		return err
	}
	members, err := orderMemberRoutes(tx, source)
	if err != nil {
		return err
	}
	states := make([]string, 0, len(members))
	for _, member := range members {
		query := tx.Where("kind = ? AND telegram_id = ? AND target_id = ? AND bot_id = ? AND chat_id = ? AND created_at >= ?", "lifecycle", source.BeneficiaryTelegramID, source.TargetID, member.BotID, member.ChatID, *lostAt).Where("route_version = ?", member.RouteVersion)
		if len(next) > 0 {
			query = query.Where("created_at < ?", next[0].PaidAt)
		}
		var operations []d.AccessOperation
		if err := query.Order("id DESC").Limit(1).Find(&operations).Error; err != nil {
			return err
		}
		state := "unconfirmed"
		if len(operations) > 0 {
			state = orderRemovalOutcome(operations[0])
		}
		// A later positive observation overrides an old ban; an unrelated kicked observation
		// is not proof that this order's removal operation succeeded.
		if member.IsMember && !member.ObservedAt.Before(*lostAt) && (len(operations) == 0 || member.ObservedAt.After(operations[0].UpdatedAt)) {
			state = "present"
		}
		states = append(states, state)
	}
	row.MemberRemoval = aggregateOrderEvidence(states)
	row.MemberRemoved = row.MemberRemoval == "removed"
	return nil
}

// orderMemberRoutes 合并本单成员与本单邀请证明的物理路由，不依赖成员最后关联的订单。
// tx 为只读事务，source 为已授权来源；返回各路由的最新成员观察，无观察时不伪造离群状态。
func orderMemberRoutes(tx *gorm.DB, source d.LedgerSource) ([]d.AccessMember, error) {
	var members []d.AccessMember
	if err := tx.Where("source_id = ? AND telegram_id = ? AND target_id = ?", source.ID, source.BeneficiaryTelegramID, source.TargetID).Find(&members).Error; err != nil {
		return nil, err
	}
	var invites []d.AccessInvite
	if err := tx.Where("source_id = ? AND telegram_id = ? AND target_id = ?", source.ID, source.BeneficiaryTelegramID, source.TargetID).Find(&invites).Error; err != nil {
		return nil, err
	}
	for _, invite := range invites {
		if invite.BotID == 0 || invite.ChatID == 0 {
			continue
		}
		found := false
		for _, member := range members {
			if member.BotID == invite.BotID && member.ChatID == invite.ChatID && member.RouteVersion == invite.RouteVersion {
				found = true
				break
			}
		}
		if found {
			continue
		}
		member := d.AccessMember{BotID: invite.BotID, ChatID: invite.ChatID, RouteVersion: invite.RouteVersion}
		var observations []d.AccessMember
		if err := tx.Where("telegram_id = ? AND target_id = ? AND bot_id = ? AND chat_id = ? AND route_version = ?", source.BeneficiaryTelegramID, source.TargetID, invite.BotID, invite.ChatID, invite.RouteVersion).Find(&observations).Error; err != nil {
			return nil, err
		}
		if len(observations) > 0 {
			member = observations[0]
		}
		members = append(members, member)
	}
	return members, nil
}

// orderRemovalOutcome returns member evidence independently of invite/unban phases.
// operation must already be scoped to this purchase's managed route and loss window.
func orderRemovalOutcome(operation d.AccessOperation) string {
	switch {
	case operation.BanConfirmed:
		return "removed"
	case operation.LifecyclePhase == "not_required":
		return "not_required"
	case operation.BanOutcome == "unknown" || operation.LifecyclePhase == "unknown_ban" || operation.State == "unknown":
		return "unknown"
	case operation.State == "failed" || operation.State == "needs_review" || operation.BanOutcome == "failed":
		return "failed"
	case operation.State == "pending" || operation.State == "running" || operation.State == "creating" || operation.State == "retryable":
		return "pending"
	default:
		return "unconfirmed"
	}
}

// PreviewRefund validates real paid item ownership, then calculates hypothetical remaining rights.
// All reads use one repeatable snapshot; no projection, refund, tombstone or external effect is written.
func (store *Store) PreviewRefund(ctx context.Context, orderID uint, items []uint) (*c.RefundPreview, error) {
	if orderID == 0 || len(items) == 0 || len(items) > 100 {
		return nil, c.ErrInvalid
	}
	selected := make(map[uint]bool, len(items))
	for _, item := range items {
		if item == 0 || selected[item] {
			return nil, c.ErrInvalid
		}
		selected[item] = true
	}
	preview := &c.RefundPreview{ObservedAt: time.Now().UTC(), Groups: []c.RefundPreviewGroup{}}
	options := &sql.TxOptions{ReadOnly: true}
	if store.db.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order oc.FulfillmentOrder
		if err := tx.Where("deleted_at IS NULL").First(&order, orderID).Error; err != nil {
			return err
		}
		if order.PaidAt == nil || order.UserID == 0 {
			return c.ErrInvalid
		}
		for _, itemID := range items {
			snapshot, err := previewPurchase(tx, order, itemID)
			if err != nil {
				return err
			}
			for _, target := range snapshot.Targets {
				effective, err := previewRemaining(tx, snapshot.BeneficiaryTelegramID, target.TargetID, selected, preview.ObservedAt)
				if err != nil {
					return err
				}
				preview.Groups = append(preview.Groups, c.RefundPreviewGroup{ItemID: itemID, TargetID: target.TargetID, ChatID: target.TelegramChatID, BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, Active: effective.Active, Permanent: effective.Kind == d.TermPermanent, ExpiresAt: effective.ExpiresAt, RemovalState: "unconfirmed", CleanupState: "unconfirmed"})
			}
		}
		return nil
	}, options)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

// previewPurchase authorizes each immutable selected purchase against the requested root/leaf order.
// Returns its original targets only after verifying paid provenance through the existing validator.
func previewPurchase(tx *gorm.DB, order oc.FulfillmentOrder, itemID uint) (*d.OrderItemSnapshot, error) {
	var item oc.FulfillmentItem
	if err := tx.Where("deleted_at IS NULL").First(&item, itemID).Error; err != nil {
		return nil, err
	}
	var leaf oc.FulfillmentOrder
	if err := tx.Where("deleted_at IS NULL").First(&leaf, item.OrderID).Error; err != nil {
		return nil, err
	}
	if leaf.UserID != order.UserID || leaf.PaidAt == nil || (leaf.ID != order.ID && (leaf.ParentID == nil || *leaf.ParentID != order.ID)) {
		return nil, c.ErrInvalid
	}
	var snapshot d.OrderItemSnapshot
	if err := tx.First(&snapshot, "order_item_id = ?", itemID).Error; err != nil {
		return nil, err
	}
	if len(snapshot.Targets) == 0 {
		return nil, c.ErrConflict
	}
	if err := validateUnpostedRefundItem(tx, leaf, item, snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// previewRemaining reuses identity mapping and the domain calculator with in-memory tombstones.
// selected IDs never mutate persisted sources; all other purchases and existing revocations remain included.
func previewRemaining(tx *gorm.DB, identity int64, target uint, selected map[uint]bool, at time.Time) (d.EntitlementProjection, error) {
	rows, revoked, mapping, err := im.Sources(tx, identity, target)
	if err != nil {
		return d.EntitlementProjection{}, err
	}
	// A paid but not yet posted purchase can change the remaining entitlement. Refuse to
	// claim "no rights" until every non-selected purchase in the mapped scope is settled.
	identities := []int64{identity}
	if mapping != nil {
		identities = []int64{mapping.OldTelegramID, mapping.NewTelegramID}
	}
	var pending []d.OrderItemSnapshot
	if err := tx.Where("beneficiary_telegram_id IN ?", identities).Find(&pending).Error; err != nil {
		return d.EntitlementProjection{}, err
	}
	posted := map[uint]bool{}
	for _, row := range rows {
		posted[row.OrderItemID] = true
	}
	for _, row := range revoked {
		posted[row.OrderItemID] = true
	}
	for _, purchase := range pending {
		if selected[purchase.OrderItemID] || posted[purchase.OrderItemID] {
			continue
		}
		for _, purchaseTarget := range purchase.Targets {
			if purchaseTarget.TargetID != target {
				continue
			}
			var count int64
			if err := tx.Table("order_items AS item").Joins("JOIN orders leaf ON leaf.id = item.order_id").Where("item.id = ? AND item.deleted_at IS NULL AND leaf.deleted_at IS NULL AND leaf.paid_at IS NOT NULL", purchase.OrderItemID).Count(&count).Error; err != nil {
				return d.EntitlementProjection{}, err
			}
			if count > 0 {
				return d.EntitlementProjection{}, c.ErrConflict
			}
		}
	}
	scope := d.EntitlementScope{BeneficiaryTelegramID: identity, TargetID: target}
	sources := make([]d.EntitlementSource, 0, len(rows))
	tombstones := make([]d.SourceKey, 0, len(revoked)+len(selected))
	for _, row := range rows {
		sources = append(sources, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
	}
	for _, row := range revoked {
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID})
	}
	for itemID := range selected {
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: itemID})
	}
	calculation, err := d.RecalculateEntitlement(scope, sources, tombstones, at)
	return calculation.Effective, err
}
