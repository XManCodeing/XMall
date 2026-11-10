package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	od "github.com/XManCodeing/XMall/internal/modules/order/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Recalculate 在 ctx 下按明确观察时刻 at 重算 scope；使用纯领域算法，不执行 Telegram 动作。
func (store *Store) Recalculate(ctx context.Context, scope d.EntitlementScope, at time.Time) (*d.EntitlementProjection, error) {
	var projection *d.EntitlementProjection
	err := store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		root, err := lockProjection(tx, scope)
		if err != nil {
			return err
		}
		calculation, err := calculateLocked(tx, root, at)
		if err != nil {
			return err
		}
		projection = &calculation.Effective
		return nil
	})
	if err != nil {
		return nil, err
	}
	return projection, nil
}

// RevokeSource 接受内部授权流程明确指定的整来源 key 和撤权时点 at，保留先到墓碑。
// 不读取退款金额、不公开 HTTP 路径；原事实不改写，重放不能复活来源。
func (store *Store) RevokeSource(ctx context.Context, key d.SourceKey, at time.Time) error {
	if key.OrderItemID == 0 || at.IsZero() {
		return c.ErrInvalid
	}
	return store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		root, err := lockProjection(tx, key.EntitlementScope)
		if err != nil {
			return err
		}
		var sources []d.LedgerSource
		if err := tx.Where("order_item_id = ? AND target_id = ?", key.OrderItemID, key.TargetID).Find(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			if source.BeneficiaryTelegramID != key.BeneficiaryTelegramID {
				return d.ErrEntitlementScope
			}
		}
		tombstone := d.LedgerRevocation{OrderItemID: key.OrderItemID, TargetID: key.TargetID, BeneficiaryTelegramID: key.BeneficiaryTelegramID, RevokedAt: at.UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&tombstone).Error; err != nil {
			return err
		}
		if err := tx.First(&tombstone, "order_item_id = ? AND target_id = ?", key.OrderItemID, key.TargetID).Error; err != nil {
			return err
		}
		if tombstone.BeneficiaryTelegramID != key.BeneficiaryTelegramID {
			return d.ErrEntitlementScope
		}
		if _, err := calculateLocked(tx, root, time.Now().UTC()); err != nil {
			return err
		}
		return insertLedgerTask(tx, root, key.OrderItemID, "revoke", time.Now().UTC())
	})
}

// CheckoutWithLedgerInTransaction 为既有 WithTx 接入真实永久重叠校验；tx 必须已打开。
// 不打开独立连接，不改变任何商品销售门控。
func CheckoutWithLedgerInTransaction(tx *gorm.DB) (c.Checkout, error) {
	store, err := InTransaction(tx)
	if err != nil {
		return nil, err
	}
	return CheckoutInTransaction(tx, store)
}

// CheckPermanentOverlap 检查 account 对应的可信 beneficiary 在 targets 上的当前永久资格。
// 资格以稳定 TG/target 隔离而非账号当前绑定；账号绑定由调用方预检验证。
func (store *Store) CheckPermanentOverlap(ctx context.Context, account uint, beneficiary int64, targets []d.SnapshotTarget) error {
	if account == 0 || beneficiary <= 0 || len(targets) == 0 {
		return c.ErrInvalid
	}
	ids := make([]uint, 0, len(targets))
	seen := make(map[uint]bool)
	for _, target := range targets {
		if target.TargetID == 0 || seen[target.TargetID] {
			return c.ErrInvalid
		}
		seen[target.TargetID] = true
		ids = append(ids, target.TargetID)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	covered := 0
	err := store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		covered = 0
		for _, id := range ids {
			root, err := lockProjection(tx, d.EntitlementScope{BeneficiaryTelegramID: beneficiary, TargetID: id})
			if err != nil {
				return err
			}
			calculation, err := calculateLocked(tx, root, time.Now().UTC())
			if err != nil {
				return err
			}
			if calculation.Effective.Active && calculation.Effective.Kind == d.TermPermanent {
				covered++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if covered == len(ids) {
		return c.ErrPermanentFullyCovered
	}
	if covered > 0 {
		return c.ErrPermanentPartiallyCovered
	}
	return nil
}

// LifecycleModels returns the extra checkpoint model; operation columns migrate with AccessModels.
func LifecycleModels() []any {
	return []any{&d.LifecycleCursor{}, &d.RefundNotice{}, &d.RefundDisposition{}, &d.Reminder{}, &d.ReminderCursor{}}
}

// ScanLifecycle scans at most limit historical members and atomically advances a fair bot cursor.
// It uses the existing all-source ledger calculation on the same transaction connection.
func (store *Store) ScanLifecycle(ctx context.Context, bot uint, limit int, at time.Time) (scanned int, err error) {
	if bot == 0 || limit < 1 || limit > 100 || at.IsZero() {
		return 0, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		scanned = 0
		cursor := d.LifecycleCursor{BotID: bot, UpdatedAt: at}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error; err != nil {
			return err
		}
		if err := dblock.Row(tx.Model(&cursor).Where("bot_id = ?", bot), "version", c.ErrNotFound); err != nil {
			return err
		}
		if err := tx.First(&cursor, "bot_id = ?", bot).Error; err != nil {
			return err
		}
		members, err := lifecycleScanMembers(tx, bot, cursor, limit)
		if err != nil {
			return err
		}
		for _, member := range members {
			if err := enqueueLifecycle(tx, member, at); err != nil {
				return err
			}
			cursor.TargetID, cursor.ChatID, cursor.TelegramID = member.TargetID, member.ChatID, member.TelegramID
			scanned++
		}
		if len(members) < limit {
			cursor.TargetID, cursor.ChatID, cursor.TelegramID = 0, 0, 0
		}
		updated := tx.Model(&d.LifecycleCursor{}).Where("bot_id = ? AND version = ?", bot, cursor.Version).
			Updates(map[string]any{"target_id": cursor.TargetID, "chat_id": cursor.ChatID, "telegram_id": cursor.TelegramID, "version": cursor.Version + 1, "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return nil
	})
	return scanned, err
}

// enqueueLifecycle creates one per-member cycle task only for a proven ledger/member association.
// Existing unfinished work owns the physical identity, even if renewal changes projection version.
// operationIDs optionally receives the exact task ID for an atomic refund disposition binding.
func enqueueLifecycle(tx *gorm.DB, member d.AccessMember, at time.Time, operationIDs ...*uint) error {
	matched, err := im.SourceMatches(tx, member.SourceID, member.TargetID, member.TelegramID)
	if err != nil {
		return err
	}
	if !matched {
		return nil
	}
	var count int64
	eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: member.TelegramID}, member.TargetID, at)
	if err != nil {
		return err
	}
	if eligible.Active {
		return nil
	}
	cycleID, err := enqueueInviteCleanup(tx, member.TargetID, member.TelegramID, at)
	if err != nil {
		return err
	}
	if member.Status == "left" || member.Status == "restricted" && !member.IsMember {
		return enqueueCleanupOnlyLifecycle(tx, member, eligible, cycleID, at, operationIDs...)
	}
	query := unfinishedLifecycleQuery(tx, member)
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		if cycleID != 0 {
			if err := query.Update("cleanup_cycle_id", cycleID).Error; err != nil {
				return err
			}
		}
		var operation d.AccessOperation
		if err := query.Order("id").First(&operation).Error; err != nil {
			return err
		}
		return captureLifecycleID(tx, operation, operationIDs...)
	}
	operation := d.AccessOperation{CleanupCycleID: cycleID, BusinessKey: fmt.Sprintf("life:%d:%d:%d:%d:%d", member.TargetID, member.ChatID, member.TelegramID, eligible.Version, member.ObservedAt.UnixNano()), Kind: "lifecycle", TelegramID: member.TelegramID, TargetID: member.TargetID, BotID: member.BotID, ChatID: member.ChatID, RouteVersion: member.RouteVersion, EntitlementVersion: eligible.Version, State: "pending", Version: 1, LifecyclePhase: "check", NextAttemptAt: at, CreatedAt: at, UpdatedAt: at}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&operation).Error; err != nil {
		return err
	}
	return captureLifecycleID(tx, operation, operationIDs...)
}

// ClaimLifecycle claims existing access work; expired side effects become read-only unknown phases.
// Selection orders by next attempt then ID so a failed low ID cannot starve later tasks.
func (store *Store) ClaimLifecycle(ctx context.Context, bot uint, token string, at time.Time, ttl time.Duration) (out *d.AccessOperation, err error) {
	if bot == 0 || len(token) < 16 || len(token) > 64 || ttl < time.Second || ttl > 5*time.Minute {
		return nil, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		out = nil
		var stale []d.AccessOperation
		if err := tx.Where("bot_id = ? AND kind = ? AND state = ? AND lease_until <= ? AND target_id NOT IN (?)", bot, "lifecycle", "creating", at, tx.Table("telegram_group_targets").Select("id").Where("recovery_task_id <> 0")).Order("id").Limit(100).Find(&stale).Error; err != nil {
			return err
		}
		for _, operation := range stale {
			phase := operation.LifecyclePhase
			if phase == "ban" {
				phase = "unknown_ban"
			}
			if phase == "unban" {
				phase = "unknown_unban"
			}
			values := map[string]any{"state": "retryable", "lifecycle_phase": phase, "version": operation.Version + 1, "error_class": "lease_lost", "next_attempt_at": at, "lease_token": "", "updated_at": at}
			if phase == "unknown_ban" {
				values["ban_outcome"] = "unknown"
			}
			if phase == "unknown_unban" {
				values["unban_outcome"] = "unknown"
			}
			if err := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ? AND state = ?", operation.ID, operation.Version, "creating").Updates(values).Error; err != nil {
				return err
			}
		}
		var operation d.AccessOperation
		err := tx.Where("bot_id = ? AND kind = ? AND state IN ? AND next_attempt_at <= ? AND target_id NOT IN (?)", bot, "lifecycle", []string{"pending", "retryable"}, at, tx.Table("telegram_group_targets").Select("id").Where("recovery_task_id <> 0")).Order("next_attempt_at, id").First(&operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		updated := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ? AND state = ?", operation.ID, operation.Version, operation.State).Updates(map[string]any{"state": "creating", "version": operation.Version + 1, "attempt": operation.Attempt + 1, "lease_token": token, "lease_until": at.Add(ttl), "last_attempt_at": at, "updated_at": at})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		operation.State, operation.Version, operation.Attempt, operation.LeaseToken, operation.LeaseUntil = "creating", operation.Version+1, operation.Attempt+1, token, at.Add(ttl)
		out = &operation
		return nil
	})
	if err == nil && out == nil {
		err = c.ErrNotFound
	}
	return out, err
}

// LifecycleEligibility validates leased historical identity and recomputes all sources on one connection.
// It never uses current account binding, preventing a rebound account from changing the kick target.
func (store *Store) LifecycleEligibility(ctx context.Context, operation *d.AccessOperation, at time.Time) (out c.AccessEligibility, err error) {
	if operation == nil || operation.Kind != "lifecycle" {
		return out, c.ErrInvalid
	}
	err = store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := accessLeaseQuery(tx, operation, at).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrConflict
		}
		out, err = trustedLifecycleEligibility(tx, operation, at)
		if err != nil || out.Active || operation.BanConfirmed || (operation.LifecyclePhase != "check" && operation.LifecyclePhase != "ban") {
			return err
		}
		var retries int64
		if err := tx.Model(&d.AccessResult{}).Where("operation_id = ? AND actor_admin_id > 0 AND action = ? AND reason = ?", operation.ID, "retry", "admin_recovery").Count(&retries).Error; err != nil {
			return err
		}
		if retries > 0 {
			pending, err := lifecyclePaidPending(tx, operation)
			if err != nil {
				return err
			}
			if pending {
				return c.ErrPaymentEvidence
			}
		}
		return nil
	})
	return out, err
}

// FinishLifecycle persists a phase, its immutable response evidence and member observation with CAS.
// Unknown bans remain unowned even when a query observes kicked; only direct success owns a ban.
func (store *Store) FinishLifecycle(ctx context.Context, operation *d.AccessOperation, outcome c.LifecycleResult, at time.Time) error {
	if operation == nil || operation.Kind != "lifecycle" {
		return c.ErrInvalid
	}
	if outcome.BanConfirmed && (operation.LifecyclePhase != "ban" || outcome.BanOutcome != "succeeded") {
		return c.ErrInvalid
	}
	receiptOutcome := outcome.Outcome
	if operation.LifecyclePhase == "ban" && outcome.BanOutcome != "" {
		receiptOutcome = outcome.BanOutcome
	}
	if operation.LifecyclePhase == "unban" && outcome.UnbanOutcome != "" {
		receiptOutcome = outcome.UnbanOutcome
	}
	receipt := d.AccessResult{OperationID: operation.ID, LeaseVersion: operation.Version, Generation: operation.Generation, Outcome: receiptOutcome, ErrorClass: accessErrorClass(outcome.ErrorClass), Action: operation.LifecyclePhase, Reason: outcome.ReviewReason, ReceivedAt: at}
	if err := store.saveAccessEvidence(ctx, operation, &receipt); err != nil {
		return err
	}
	return store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		state := outcome.Outcome
		if state != "pending" && state != "retryable" && state != "succeeded" && state != "needs_review" {
			state = "needs_review"
		}
		delay := accessReconcileDelay(operation.Attempt, outcome.RetryAfter)
		next := at
		if state == "retryable" {
			next = at.Add(delay)
		}
		values := map[string]any{"state": state, "lifecycle_phase": outcome.Phase, "version": operation.Version + 1, "lease_token": "", "next_attempt_at": next, "review_reason": outcome.ReviewReason, "error_class": accessErrorClass(outcome.ErrorClass), "updated_at": at}
		if outcome.BanOutcome != "" {
			values["ban_outcome"] = outcome.BanOutcome
		}
		if outcome.UnbanOutcome != "" {
			values["unban_outcome"] = outcome.UnbanOutcome
		}
		if outcome.BanConfirmed {
			// Keep the precise local acknowledgement. External Telegram dates are
			// second-wide intervals in blockExternalLifecycleBan, never forged instants.
			values["ban_confirmed"], values["ban_confirmed_at"] = true, at
		}
		if outcome.Member != nil {
			values["member_status"], values["member_is_member"], values["observed_at"] = outcome.Member.Status, outcome.Member.IsMember, outcome.ObservedAt
		}
		eligible, err := accessEligibility(tx, c.AccessIdentity{TelegramID: operation.TelegramID}, operation.TargetID, at)
		if err != nil {
			return err
		}
		values["entitlement_version"] = eligible.Version
		if state == "succeeded" && operation.BanConfirmed && eligible.Active {
			values["review_reason"] = "admission_restored"
		}
		updated := accessLeaseQuery(tx, operation, at).Updates(values)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		if err := im.Complete(tx, operation, outcome, at); err != nil {
			return err
		}
		if outcome.Member != nil && outcome.Member.Outcome == c.AccessSucceeded {
			_, err := observeAccess(tx, c.AccessObservation{TargetID: operation.TargetID, BotID: operation.BotID, ChatID: operation.ChatID, RouteVersion: operation.RouteVersion, TelegramID: operation.TelegramID, Status: outcome.Member.Status, IsMember: outcome.Member.IsMember, ObservedAt: outcome.ObservedAt, Source: "query"})
			return err
		}
		return nil
	})
}

// ListLifecycle returns a bounded secret-free admin view for bot, after the supplied task ID.
func (store *Store) ListLifecycle(ctx context.Context, bot, after uint, limit int) ([]c.LifecycleTask, error) {
	if bot == 0 || limit < 1 || limit > 100 {
		return nil, c.ErrInvalid
	}
	var rows []c.LifecycleTask
	err := store.db.WithContext(ctx).Model(&d.AccessOperation{}).Where("bot_id = ? AND kind = ? AND id > ?", bot, "lifecycle", after).Order("id").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for index := range rows {
		if err := store.projectInviteCleanup(ctx, &rows[index]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// LifecycleTasksPage returns the filtered COUNT and one real OFFSET page for bot.
// Only selected rows receive cleanup projections; cursor readers and summary remain unchanged.
func (store *Store) LifecycleTasksPage(ctx context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleTask, int64, error) {
	states, err := c.LifecycleFilterStates(status...)
	if bot == 0 || !c.ValidAdminPage(page, size) || err != nil {
		return nil, 0, c.ErrInvalid
	}
	rows := []c.LifecycleTask{}
	var total int64
	query := store.db.WithContext(ctx).Model(&d.AccessOperation{}).Where("bot_id = ? AND kind = ?", bot, "lifecycle")
	if len(states) > 0 {
		query = query.Where("state IN ?", states)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	for index := range rows {
		if err := store.projectInviteCleanup(ctx, &rows[index]); err != nil {
			return nil, 0, err
		}
	}
	return rows, total, nil
}

// RecoverLifecycle audits explicit administrator recovery without asserting any platform outcome.
// Recheck never retries an unknown effect; block_ban permanently prevents automatic unban.
func (store *Store) RecoverLifecycle(ctx context.Context, input c.LifecycleRecovery, at time.Time) error {
	if input.BotID == 0 || input.ID == 0 || input.ExpectedVersion == 0 || input.ActorAdminID == 0 {
		return c.ErrInvalid
	}
	return store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		var operation d.AccessOperation
		if err := tx.First(&operation, "id = ? AND bot_id = ? AND kind = ?", input.ID, input.BotID, "lifecycle").Error; err != nil {
			return err
		}
		if operation.Version != input.ExpectedVersion || operation.State == "creating" && input.Action != "block_ban" {
			return c.ErrConflict
		}
		values := map[string]any{"version": operation.Version + 1, "updated_at": at, "next_attempt_at": at}
		switch input.Action {
		case "block_ban":
			values["ban_blocked"], values["state"], values["review_reason"] = true, "needs_review", "disciplinary_ban"
		case "recheck":
			if operation.State != "needs_review" {
				return c.ErrConflict
			}
			phase := "verify"
			if operation.BanOutcome == "unknown" {
				phase = "unknown_ban"
			}
			if operation.UnbanOutcome == "unknown" {
				phase = "unknown_unban"
			}
			values["lifecycle_phase"], values["state"] = phase, "pending"
		case "retry":
			if operation.State != "needs_review" || operation.BanBlocked || operation.UnbanOutcome == "succeeded" || operation.BanOutcome == "unknown" || operation.UnbanOutcome == "unknown" {
				return c.ErrConflict
			}
			phase := "check"
			if operation.BanConfirmed {
				phase = "unban"
			}
			values["lifecycle_phase"], values["state"] = phase, "pending"
			if err := recoverLifecycleEligibility(tx, &operation, values, at); err != nil {
				return err
			}
		default:
			return c.ErrInvalid
		}
		updated := tx.Model(&d.AccessOperation{}).Where("id = ? AND version = ?", operation.ID, operation.Version).Updates(values)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return c.ErrConflict
		}
		return tx.Create(&d.AccessResult{OperationID: operation.ID, LeaseVersion: operation.Version + 1, Generation: operation.Generation, ActorAdminID: input.ActorAdminID, Action: input.Action, Outcome: "reviewed", Reason: "admin_recovery", ReceivedAt: at}).Error
	})
}

var _ c.LifecycleRepository = (*Store)(nil)

const systemFulfillmentKey = "membership:system-fulfillment"

// LedgerModels 返回 v16 迁移所需的全部账本模型；每次调用返回独立实例，不执行 DDL。
func LedgerModels() []any {
	return []any{&d.LedgerSource{}, &d.LedgerRevocation{}, &d.LedgerProjection{}, &d.LedgerOutbox{}, &d.IdentityMigration{}}
}

// WithSystemFulfillment 返回装配 factory 的独立仓储；factory 必须仅使用传入事务。
// 不修改原仓储，不允许 nil 端口静默完成会员履约。
func (store *Store) WithSystemFulfillment(factory func(*gorm.DB) fc.MembershipSystemStore) *Store {
	return &Store{db: store.db.Set(systemFulfillmentKey, factory), boundTransaction: store.boundTransaction}
}

// GrantPaid 使用 input 中固定业务时间，从真实订单及快照原子授予；任何失败完整回滚。
// 只重试并发冲突/SQLite 忙，不在事务中访问全局连接或外部网络。
func (store *Store) GrantPaid(ctx context.Context, input c.PaidGrant) error {
	if input.OrderID == 0 || input.PaidAt.IsZero() {
		return c.ErrInvalid
	}
	err := store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		lines, paymentOrder, err := loadGrantLines(tx, input.OrderID)
		if err != nil || len(lines) == 0 {
			return err
		}
		if err := validateGrantPayment(tx, paymentOrder, input); err != nil {
			return err
		}
		value, ok := store.db.Get(systemFulfillmentKey)
		factory, valid := value.(func(*gorm.DB) fc.MembershipSystemStore)
		if !ok || !valid || factory == nil {
			return c.ErrSystemFulfillmentUnavailable
		}
		fulfillment := factory(tx)
		if fulfillment == nil {
			return c.ErrSystemFulfillmentUnavailable
		}
		items, err := persistGrantLines(tx, lines, paymentOrder.ID, input, time.Now().UTC())
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		return fulfillment.CompleteMembership(ctx, items, input.PaidAt.UTC())
	})
	if err != nil {
		slog.WarnContext(ctx, "membership_grant_failed", "order_id", input.OrderID)
	} else {
		slog.InfoContext(ctx, "membership_grant_committed", "order_id", input.OrderID)
	}
	return err
}

// ledgerTransaction 在 ctx 下执行短事务 fn；冲突最多重试四次，取消立即停止。
func (store *Store) ledgerTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = store.transaction(ctx, fn)
		if err == nil || !retryLedger(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

// retryLedger 判断 err 是否为可安全整事务重试的唯一键/CAS/数据库竞争，不记录原始 SQL。
func retryLedger(err error) bool {
	text := strings.ToLower(err.Error())
	return errors.Is(err, c.ErrConflict) || strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked") || strings.Contains(text, "deadlock detected") || strings.Contains(text, "could not serialize")
}

// lockProjection 在 tx 创建并锁定 scope 的串行化根，跨方言 INSERT ON CONFLICT+CAS 写锁。
// SQLite 忽略 FOR UPDATE，因此额外执行无值变化 UPDATE 获取写锁；首次插入竞争由唯一主键解决。
func lockProjection(tx *gorm.DB, scope d.EntitlementScope) (*d.LedgerProjection, error) {
	if scope.BeneficiaryTelegramID <= 0 || scope.TargetID == 0 {
		return nil, c.ErrInvalid
	}
	identity, err := im.Scope(tx, scope.BeneficiaryTelegramID, scope.TargetID)
	if err != nil {
		return nil, err
	}
	scope.BeneficiaryTelegramID = identity
	row := d.LedgerProjection{BeneficiaryTelegramID: scope.BeneficiaryTelegramID, TargetID: scope.TargetID, CalculatedAt: time.Unix(0, 0).UTC()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	query := tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id = ?", scope.BeneficiaryTelegramID, scope.TargetID)
	if err := dblock.Row(query, "version", c.ErrNotFound); err != nil {
		return nil, err
	}
	if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row).Error; err != nil {
		return nil, err
	}
	latest, err := im.Scope(tx, scope.BeneficiaryTelegramID, scope.TargetID)
	if err != nil {
		return nil, err
	}
	if latest != scope.BeneficiaryTelegramID {
		return lockProjection(tx, d.EntitlementScope{BeneficiaryTelegramID: latest, TargetID: scope.TargetID})
	}
	return &row, nil
}

// calculateLocked 从 tx 全部不可变来源和墓碑调用纯领域重算；root 必须已锁定。
func calculateLocked(tx *gorm.DB, root *d.LedgerProjection, at time.Time) (*d.EntitlementCalculation, error) {
	scope := d.EntitlementScope{BeneficiaryTelegramID: root.BeneficiaryTelegramID, TargetID: root.TargetID}
	rows, revoked, _, err := im.Sources(tx, scope.BeneficiaryTelegramID, scope.TargetID)
	if err != nil {
		return nil, err
	}
	sources := make([]d.EntitlementSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, d.EntitlementSource{Key: d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID}, PaidAt: row.PaidAt, Kind: row.Kind, Months: row.Months, State: d.SourceActive})
	}
	tombstones := make([]d.SourceKey, 0, len(revoked))
	for _, row := range revoked {
		tombstones = append(tombstones, d.SourceKey{EntitlementScope: scope, OrderItemID: row.OrderItemID})
	}
	calculation, err := d.RecalculateEntitlement(scope, sources, tombstones, at)
	if err != nil {
		return nil, err
	}
	if root.Version >= math.MaxInt64 {
		return nil, fmt.Errorf("membership projection version overflow")
	}
	updated := tx.Model(&d.LedgerProjection{}).Where("beneficiary_telegram_id = ? AND target_id = ? AND version = ?", scope.BeneficiaryTelegramID, scope.TargetID, root.Version).Updates(map[string]any{"version": root.Version + 1, "active": calculation.Effective.Active, "kind": calculation.Effective.Kind, "expires_at": calculation.Effective.ExpiresAt, "limited_expires_at": calculation.LimitedExpiresAt, "calculated_at": at.UTC()})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, c.ErrConflict
	}
	root.Version++
	return &calculation, nil
}

var _ c.PaidGranter = (*Store)(nil)
var _ c.Ledger = (*Store)(nil)
var _ c.PermanentOverlapGuard = (*Store)(nil)

// grantLine 将真实订单项及不可变快照保持在一起，不查询商品当前配置。
type grantLine struct {
	item     od.FulfillmentItem
	order    od.FulfillmentOrder
	snapshot d.OrderItemSnapshot
}

// loadGrantLines 从 tx 加载 orderID 的实际会员项及付款聚合根，子事件不重复消费兄弟项。
func loadGrantLines(tx *gorm.DB, orderID uint) ([]grantLine, *od.FulfillmentOrder, error) {
	var requested od.FulfillmentOrder
	if err := tx.First(&requested, "id = ? AND deleted_at IS NULL", orderID).Error; err != nil {
		return nil, nil, err
	}
	rootID := requested.ID
	if requested.ParentID != nil {
		rootID = *requested.ParentID
	}
	var root od.FulfillmentOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, "id = ? AND deleted_at IS NULL", rootID).Error; err != nil {
		return nil, nil, err
	}
	var orders []od.FulfillmentOrder
	if requested.ParentID == nil {
		if err := tx.Where("(id = ? OR parent_id = ?) AND deleted_at IS NULL", rootID, rootID).Order("id").Find(&orders).Error; err != nil {
			return nil, nil, err
		}
	} else {
		if err := tx.First(&requested, "id = ? AND parent_id = ? AND deleted_at IS NULL", orderID, rootID).Error; err != nil {
			return nil, nil, err
		}
		orders = []od.FulfillmentOrder{requested}
	}
	var lines []grantLine
	for _, order := range orders {
		var items []od.FulfillmentItem
		if err := tx.Where("order_id = ? AND deleted_at IS NULL", order.ID).Order("id").Find(&items).Error; err != nil {
			return nil, nil, err
		}
		for _, item := range items {
			var snapshot d.OrderItemSnapshot
			err := tx.First(&snapshot, "order_item_id = ?", item.ID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if err := validateRefundFacts(tx, order); err != nil {
				return nil, nil, err
			}
			if err := validateGrantLine(order, root, item, snapshot); err != nil {
				return nil, nil, err
			}
			lines = append(lines, grantLine{item: item, order: order, snapshot: snapshot})
		}
	}
	ids := make([]uint, 0, len(orders))
	present := make(map[uint]bool, len(lines))
	for _, line := range lines {
		present[line.item.ID] = true
	}
	for _, order := range orders {
		ids = append(ids, order.ID)
	}
	// 历史快照部分或全部被删除均不能伪装成普通订单成功。
	var existing []d.LedgerSource
	if err := tx.Where("order_id IN ?", ids).Find(&existing).Error; err != nil {
		return nil, nil, err
	}
	for _, source := range existing {
		if !present[source.OrderItemID] {
			return nil, nil, d.ErrSourceConflict
		}
	}
	return lines, &root, nil
}

// validateRefundFacts rejects fabricated refunded flags, without using valid financial refunds as revocation policy.
// Parent totals may contain child refunds; child display statuses may inherit a parent's refund.
func validateRefundFacts(tx *gorm.DB, order od.FulfillmentOrder) error {
	if order.RefundedAmount.Decimal.IsZero() && order.Status != constants.OrderStatusRefunded && order.Status != constants.OrderStatusPartiallyRefunded {
		return nil
	}
	if !tx.Migrator().HasTable(&od.RefundFact{}) {
		return c.ErrPaymentEvidence
	}
	var records []od.RefundFact
	query := tx.Where("deleted_at IS NULL AND user_id = ? AND type IN ?", order.UserID, []string{constants.OrderRefundTypeManual, constants.OrderRefundTypeWallet})
	if order.ParentID == nil {
		query = query.Where("order_id = ? OR order_id IN (SELECT id FROM orders WHERE parent_id = ? AND user_id = ? AND deleted_at IS NULL)", order.ID, order.ID, order.UserID)
	} else {
		query = query.Where("order_id = ? OR order_id = ?", order.ID, *order.ParentID)
	}
	if err := query.Find(&records).Error; err != nil {
		return err
	}
	total := money.Amount{}
	for _, record := range records {
		if record.CreatedAt.IsZero() || !record.Amount.Decimal.IsPositive() {
			return c.ErrPaymentEvidence
		}
		total = money.FromDecimal(total.Decimal.Add(record.Amount.Decimal))
	}
	if !total.Decimal.IsPositive() || total.Decimal.LessThan(order.RefundedAmount.Decimal) {
		return c.ErrPaymentEvidence
	}
	return nil
}

// grantableOrder accepts paid states; valid monetary refunds never revoke entitlement without tombstones.
func grantableOrder(order od.FulfillmentOrder) bool {
	if order.PaidAt == nil || order.PaidAt.IsZero() || order.DeletedAt != nil || order.CanceledAt != nil {
		return false
	}
	switch order.Status {
	case constants.OrderStatusPaid, constants.OrderStatusFulfilling, constants.OrderStatusPartiallyDelivered, constants.OrderStatusDelivered, constants.OrderStatusCompleted, constants.OrderStatusPartiallyRefunded, constants.OrderStatusRefunded:
		return true
	default:
		return false
	}
}

// validateGrantLine 校验 order/root/item/snapshot 的购买归属和期限，失败返回明确合同错误。
func validateGrantLine(order, root od.FulfillmentOrder, item od.FulfillmentItem, snapshot d.OrderItemSnapshot) error {
	if !grantableOrder(order) || !grantableOrder(root) || !order.PaidAt.Equal(*root.PaidAt) || order.UserID == 0 || order.UserID != root.UserID || order.UserID != snapshot.AccountID {
		return c.ErrPaymentEvidence
	}
	if item.OrderID != order.ID || item.ProductID != snapshot.ProductID || item.SKUID != snapshot.SKUID || item.Quantity != 1 || snapshot.Quantity != 1 || snapshot.BeneficiaryTelegramID <= 0 || snapshot.IdentityVersion == 0 || snapshot.ConfigVersion == 0 || len(snapshot.Targets) == 0 {
		return c.ErrInvalid
	}
	seconds, err := d.DurationSeconds(snapshot.Kind, snapshot.Months)
	if err != nil || seconds != snapshot.DurationSeconds {
		return c.ErrInvalid
	}
	seen := make(map[uint]bool)
	for _, target := range snapshot.Targets {
		if target.TargetID == 0 || seen[target.TargetID] || target.RouteVersion == 0 || target.BotInstanceID == 0 || target.TelegramChatID == 0 {
			return c.ErrInvalid
		}
		seen[target.TargetID] = true
	}
	return nil
}

// validateGrantPayment 验证 tx 中持久支付成功事实；input 时间必须等于成功记录而非执行时钟。
// 订单 PaidAt 在历史流程中可能是处理时点，网关路径以支付记录 PaidAt 为准；钱包以订单为准。
func validateGrantPayment(tx *gorm.DB, root *od.FulfillmentOrder, input c.PaidGrant) error {
	if !grantableOrder(*root) || root.ParentID != nil {
		return c.ErrPaymentEvidence
	}
	if err := validateRefundFacts(tx, *root); err != nil {
		return err
	}
	if input.PaymentID == 0 {
		return validateWalletGrant(tx, root, input.PaidAt)
	}
	var payment grantPaymentEvidence
	if err := tx.Table("payments").Where("id = ?", input.PaymentID).Take(&payment).Error; err != nil {
		return err
	}
	if payment.OrderID != root.ID || payment.Status != constants.PaymentStatusSuccess || payment.PaidAt == nil || !payment.PaidAt.Equal(input.PaidAt) || payment.DeletedAt != nil || payment.ExceptionCode != "" || !validGrantCurrency(payment, root) {
		return c.ErrPaymentEvidence
	}
	// 钱包也有真实非零支付记录；必须核对扣款流水，而非误判为在线收款。
	if payment.ProviderType == constants.PaymentProviderWallet {
		return validateWalletGrant(tx, root, input.PaidAt)
	}
	if !root.OnlinePaidAmount.Decimal.IsPositive() || !root.OnlinePaidAmount.Decimal.Add(root.WalletPaidAmount.Decimal).Equal(root.TotalAmount.Decimal) {
		return c.ErrPaymentEvidence
	}
	return nil
}

// validateWalletGrant 核对 root 的全额钱包支付及真实扣款流水，paidAt 必须匹配订单付款时点。
func validateWalletGrant(tx *gorm.DB, root *od.FulfillmentOrder, paidAt time.Time) error {
	if root.PaidAt == nil || !root.PaidAt.Equal(paidAt) || !root.TotalAmount.Decimal.IsPositive() || !root.WalletPaidAmount.Decimal.Equal(root.TotalAmount.Decimal) || !root.OnlinePaidAmount.Decimal.IsZero() {
		return c.ErrPaymentEvidence
	}
	var movements []struct{ Amount money.Amount }
	if err := tx.Table("wallet_transactions").Select("amount").Where("order_id = ? AND user_id = ? AND type = ? AND direction = ? AND currency = ? AND deleted_at IS NULL", root.ID, root.UserID, constants.WalletTxnTypeOrderPay, constants.WalletTxnDirectionOut, root.Currency).Find(&movements).Error; err != nil {
		return err
	}
	total := money.Amount{}
	for _, movement := range movements {
		total = money.FromDecimal(total.Decimal.Add(movement.Amount.Decimal))
	}
	if !total.Decimal.Equal(root.TotalAmount.Decimal) {
		return c.ErrPaymentEvidence
	}
	return nil
}

// persistGrantLines 按 TG/target 稳定顺序串行写入 lines、重算和恢复任务；返回实际订单项 IDs。
func persistGrantLines(tx *gorm.DB, lines []grantLine, paymentOrderID uint, input c.PaidGrant, now time.Time) ([]uint, error) {
	type contribution struct {
		line   grantLine
		target uint
	}
	var contributions []contribution
	var items []uint
	for _, line := range lines {
		items = append(items, line.item.ID)
		for _, target := range line.snapshot.Targets {
			contributions = append(contributions, contribution{line: line, target: target.TargetID})
		}
	}
	sort.Slice(contributions, func(left, right int) bool {
		first, second := contributions[left], contributions[right]
		if first.line.snapshot.BeneficiaryTelegramID != second.line.snapshot.BeneficiaryTelegramID {
			return first.line.snapshot.BeneficiaryTelegramID < second.line.snapshot.BeneficiaryTelegramID
		}
		if first.target != second.target {
			return first.target < second.target
		}
		return first.line.item.ID < second.line.item.ID
	})
	for _, contribution := range contributions {
		snapshot := contribution.line.snapshot
		root, err := lockProjection(tx, d.EntitlementScope{BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID, TargetID: contribution.target})
		if err != nil {
			return nil, err
		}
		if err := insertGrantSource(tx, contribution.line, contribution.target, paymentOrderID, input, now); err != nil {
			return nil, err
		}
		if _, err := calculateLocked(tx, root, now); err != nil {
			return nil, err
		}
		var revoked int64
		if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ? AND target_id = ?", contribution.line.item.ID, contribution.target).Count(&revoked).Error; err != nil {
			return nil, err
		}
		if revoked == 0 {
			if err := insertLedgerTask(tx, root, contribution.line.item.ID, "grant", now); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(items, func(left, right int) bool { return items[left] < items[right] })
	// A late grant records historical payment facts, but a revoked purchase must not emit an opening receipt.
	eligibleItems := make([]uint, 0, len(items))
	for _, itemID := range items {
		var revoked int64
		if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ?", itemID).Count(&revoked).Error; err != nil {
			return nil, err
		}
		if revoked == 0 {
			eligibleItems = append(eligibleItems, itemID)
		}
	}
	return eligibleItems, nil
}

// insertGrantSource 插入 line 的 target 来源或验证已有不可变事实，绝不更新历史。
func insertGrantSource(tx *gorm.DB, line grantLine, target, paymentOrderID uint, input c.PaidGrant, now time.Time) error {
	var tombstones []d.LedgerRevocation
	if err := tx.Where("order_item_id = ? AND target_id = ?", line.item.ID, target).Find(&tombstones).Error; err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if tombstone.BeneficiaryTelegramID != line.snapshot.BeneficiaryTelegramID {
			return d.ErrEntitlementScope
		}
	}
	encoded, err := json.Marshal(line.snapshot)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	// 同一订单项替换全部目标时也必须拒绝，不能仅比较当前目标的唯一键。
	var history []d.LedgerSource
	if err := tx.Where("order_item_id = ?", line.item.ID).Find(&history).Error; err != nil {
		return err
	}
	for _, previous := range history {
		if previous.SnapshotHash != hash {
			return d.ErrSourceConflict
		}
	}
	source := d.LedgerSource{OrderItemID: line.item.ID, TargetID: target, Operation: "grant", BeneficiaryTelegramID: line.snapshot.BeneficiaryTelegramID, AccountID: line.snapshot.AccountID, OrderID: line.order.ID, PaymentOrderID: paymentOrderID, PaymentID: input.PaymentID, PaidAt: input.PaidAt.UTC(), Kind: line.snapshot.Kind, Months: line.snapshot.Months, SnapshotHash: hash, CreatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&source).Error; err != nil {
		return err
	}
	var existing d.LedgerSource
	if err := tx.Where("order_item_id = ? AND target_id = ? AND operation = ?", line.item.ID, target, "grant").First(&existing).Error; err != nil {
		return err
	}
	if existing.BeneficiaryTelegramID != source.BeneficiaryTelegramID || existing.AccountID != source.AccountID || existing.OrderID != source.OrderID || existing.PaymentOrderID != source.PaymentOrderID || existing.PaymentID != source.PaymentID || !existing.PaidAt.Equal(source.PaidAt) || existing.Kind != source.Kind || existing.Months != source.Months || existing.SnapshotHash != source.SnapshotHash {
		return d.ErrSourceConflict
	}
	return nil
}

// insertLedgerTask 在 tx 插入 root 的 itemID/operation 恢复任务，重放保留已有执行状态。
func insertLedgerTask(tx *gorm.DB, root *d.LedgerProjection, itemID uint, operation string, now time.Time) error {
	task := d.LedgerOutbox{OrderItemID: itemID, TargetID: root.TargetID, Operation: operation, BeneficiaryTelegramID: root.BeneficiaryTelegramID, ProjectionVersion: root.Version, State: "pending", AvailableAt: now, CreatedAt: now, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&task).Error
}
