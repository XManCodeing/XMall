package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"fmt"
	"github.com/XManCodeing/XMall/internal/constants"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	im "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore/identitymigration"
	"github.com/XManCodeing/XMall/internal/platform/database/dblock"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// grantPaymentEvidence reads persisted successful payment facts without gateway configuration.
type grantPaymentEvidence struct {
	ID, OrderID                                   uint
	Status, ExceptionCode, Currency, ProviderType string
	PaidAt, DeletedAt                             *time.Time
	Amount, FeeAmount                             money.Amount
	FeePolicy                                     string
	ProviderPayload                               jsonmap.JSON `gorm:"type:json"`
}

// validGrantCurrency accepts settlement currency only with the original currency and
// amount snapshot. Payment success already proves callback coverage; never compare
// settlement units directly to the order or recalculate from mutable channel settings.
func validGrantCurrency(payment grantPaymentEvidence, order *oc.FulfillmentOrder) bool {
	if payment.Currency == order.Currency && order.Currency != "" {
		return true
	}
	if payment.ProviderType == constants.PaymentProviderWallet || payment.Currency == "" || order.Currency == "" || !payment.Amount.Decimal.IsPositive() {
		return false
	}
	if fmt.Sprint(payment.ProviderPayload["original_currency"]) != order.Currency {
		return false
	}
	rate, rateErr := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(payment.ProviderPayload["exchange_rate"])))
	original, amountErr := decimal.NewFromString(strings.TrimSpace(fmt.Sprint(payment.ProviderPayload["original_amount"])))
	if rateErr != nil || amountErr != nil || !rate.IsPositive() || !original.IsPositive() || payment.FeeAmount.Decimal.IsNegative() {
		return false
	}
	// The original amount includes customer-paid fees, but merchant fees do not
	// reduce the order coverage. This is the payment module's stored fee policy.
	switch payment.FeePolicy {
	case constants.PaymentFeePolicyCustomerSurcharge, constants.PaymentFeePolicyLegacyCustomerSurcharge, "":
		original = original.Sub(payment.FeeAmount.Decimal)
	case constants.PaymentFeePolicyNone, constants.PaymentFeePolicyMerchantAbsorbed:
	default:
		return false
	}
	return original.Round(2).Equal(order.OnlinePaidAmount.Decimal)
}

var _ c.PaidEventAppender = (*Store)(nil)
var _ c.PaidEventStore = (*Store)(nil)

// RefundCandidates reads only durable local refund events related to the requested real order.
// orderID is the authorized detail scope; parent refunds expose children without attributing money.
func (store *Store) RefundCandidates(ctx context.Context, orderID uint) ([]c.RefundCandidate, error) {
	if orderID == 0 {
		return nil, c.ErrInvalid
	}
	rows := []c.RefundCandidate{}
	err := store.transaction(ctx, func(tx *gorm.DB) error {
		var notices []d.RefundNotice
		if err := tx.Model(&d.RefundNotice{}).Joins("JOIN orders requested ON requested.id = ?", orderID).
			Where("telegram_membership_refund_notices.order_id = requested.id OR telegram_membership_refund_notices.order_id = requested.parent_id OR telegram_membership_refund_notices.order_id IN (SELECT id FROM orders WHERE parent_id = requested.id)").Order("refund_id").Find(&notices).Error; err != nil {
			return err
		}
		for _, notice := range notices {
			var items []uint
			if err := tx.Table("telegram_membership_order_item_snapshots AS snapshot").Joins("JOIN order_items item ON item.id = snapshot.order_item_id").Joins("JOIN orders leaf ON leaf.id = item.order_id").Where("item.deleted_at IS NULL AND leaf.deleted_at IS NULL AND (leaf.id = ? OR leaf.parent_id = ?) AND (leaf.id = ? OR leaf.parent_id = ?)", notice.OrderID, notice.OrderID, orderID, orderID).Order("item.id").Pluck("item.id", &items).Error; err != nil {
				return err
			}
			for _, itemID := range items {
				candidate, _, err := refundCandidate(tx, notice.RefundID, itemID)
				if err != nil {
					return err
				}
				rows = append(rows, *candidate)
			}
		}
		return nil
	})
	return rows, err
}

// refundCandidate verifies refund type, owner, actual item-parent relation, immutable snapshot and sources.
// tx must be the caller's transaction; order status and refund amount never determine entitlement policy.
func refundCandidate(tx *gorm.DB, refundID, itemID uint) (*c.RefundCandidate, []d.LedgerSource, error) {
	var refund oc.RefundFact
	if err := tx.Where("deleted_at IS NULL").First(&refund, refundID).Error; err != nil {
		return nil, nil, err
	}
	if refund.Type != constants.OrderRefundTypeManual && refund.Type != constants.OrderRefundTypeWallet {
		return nil, nil, c.ErrInvalid
	}
	if !refund.Amount.Decimal.IsPositive() || refund.CreatedAt.IsZero() || refund.UserID == 0 {
		return nil, nil, c.ErrInvalid
	}
	var item oc.FulfillmentItem
	if err := tx.Where("deleted_at IS NULL").First(&item, itemID).Error; err != nil {
		return nil, nil, err
	}
	var order oc.FulfillmentOrder
	if err := tx.Where("deleted_at IS NULL").First(&order, item.OrderID).Error; err != nil {
		return nil, nil, err
	}
	if order.UserID != refund.UserID || order.PaidAt == nil || (order.ID != refund.OrderID && (order.ParentID == nil || *order.ParentID != refund.OrderID)) {
		return nil, nil, c.ErrInvalid
	}
	var refundOrder oc.FulfillmentOrder
	if err := tx.Where("deleted_at IS NULL").First(&refundOrder, refund.OrderID).Error; err != nil {
		return nil, nil, err
	}
	if refundOrder.UserID != order.UserID || refundOrder.PaidAt == nil || refundOrder.RefundedAmount.Decimal.LessThan(refund.Amount.Decimal) {
		return nil, nil, c.ErrInvalid
	}
	var snapshot d.OrderItemSnapshot
	if err := tx.First(&snapshot, "order_item_id = ?", itemID).Error; err != nil {
		return nil, nil, err
	}
	if snapshot.AccountID != order.UserID || snapshot.ProductID != item.ProductID || snapshot.SKUID != item.SKUID || snapshot.BeneficiaryTelegramID <= 0 {
		return nil, nil, c.ErrConflict
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	var sources []d.LedgerSource
	if err := tx.Where("order_item_id = ?", itemID).Order("target_id").Find(&sources).Error; err != nil {
		return nil, nil, err
	}
	if len(snapshot.Targets) == 0 || len(sources) > 0 && len(sources) != len(snapshot.Targets) {
		return nil, nil, c.ErrConflict
	}
	if len(sources) == 0 {
		if err := validateUnpostedRefundItem(tx, order, item, snapshot); err != nil {
			return nil, nil, err
		}
	}
	allowed := map[uint]bool{}
	for _, target := range snapshot.Targets {
		if target.TargetID == 0 || allowed[target.TargetID] {
			return nil, nil, c.ErrConflict
		}
		allowed[target.TargetID] = true
	}
	candidate := &c.RefundCandidate{RefundSelection: c.RefundSelection{RefundID: refundID, OrderItemID: itemID, AccountID: order.UserID, ExpectedVersion: 1, SnapshotHash: hash, SourceIDs: []uint{}}, OrderID: order.ID, BeneficiaryTelegramID: snapshot.BeneficiaryTelegramID}
	paymentOrderID := order.ID
	if order.ParentID != nil {
		paymentOrderID = *order.ParentID
	}
	for _, source := range sources {
		if source.PaymentOrderID != paymentOrderID || !allowed[source.TargetID] || source.SnapshotHash != hash || source.AccountID != order.UserID || source.OrderID != order.ID || source.BeneficiaryTelegramID != snapshot.BeneficiaryTelegramID || source.Kind != snapshot.Kind || source.Months != snapshot.Months || source.Operation != "grant" {
			return nil, nil, c.ErrConflict
		}
		delete(allowed, source.TargetID)
		candidate.SourceIDs = append(candidate.SourceIDs, source.ID)
	}
	sort.Slice(candidate.SourceIDs, func(left, right int) bool { return candidate.SourceIDs[left] < candidate.SourceIDs[right] })
	var count int64
	if err := tx.Model(&d.RefundDisposition{}).Where("refund_id = ? AND order_item_id = ?", refundID, itemID).Count(&count).Error; err != nil {
		return nil, nil, err
	}
	var notice d.RefundNotice
	if err := tx.First(&notice, "refund_id = ?", refundID).Error; err != nil {
		return nil, nil, err
	}
	candidate.ExpectedVersion = notice.Version
	candidate.Disposed = count > 0
	return candidate, sources, nil
}

// lockRefundPurchase serializes a disposition against late grants using their shared payment-root lock.
// itemID is an actual item; only transaction-local queries are allowed.
func lockRefundPurchase(tx *gorm.DB, itemID uint) error {
	var item oc.FulfillmentItem
	if err := tx.First(&item, "id = ? AND deleted_at IS NULL", itemID).Error; err != nil {
		return err
	}
	var order oc.FulfillmentOrder
	if err := tx.First(&order, "id = ? AND deleted_at IS NULL", item.OrderID).Error; err != nil {
		return err
	}
	rootID := order.ID
	if order.ParentID != nil {
		rootID = *order.ParentID
	}
	return dblock.Row(tx.Model(&oc.FulfillmentOrder{}).Where("id = ? AND deleted_at IS NULL", rootID), "status", c.ErrNotFound)
}

// validateUnpostedRefundItem verifies the original payment, not the refund, before allowing an early tombstone.
// All facts use tx; absence of sources is never replaced with fabricated source rows.
func validateUnpostedRefundItem(tx *gorm.DB, order oc.FulfillmentOrder, item oc.FulfillmentItem, snapshot d.OrderItemSnapshot) error {
	rootID := order.ID
	if order.ParentID != nil {
		rootID = *order.ParentID
	}
	var root oc.FulfillmentOrder
	if err := tx.First(&root, "id = ? AND deleted_at IS NULL", rootID).Error; err != nil {
		return err
	}
	if err := validateGrantLine(order, root, item, snapshot); err != nil {
		return err
	}
	var fact paidFact
	if err := paidFacts(tx).Where("p.order_id = ?", rootID).Order("p.id").Take(&fact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return validateWalletGrant(tx, &root, *root.PaidAt)
		}
		return err
	}
	return validateGrantPayment(tx, &root, c.PaidGrant{OrderID: rootID, PaymentID: fact.PaymentID, PaidAt: fact.PaidAt})
}

// DisposeRefund atomically revokes all targets of the selected purchase, records audit and queues lifecycle.
// input must carry authenticated actor, exact source IDs/hash and command version; no network occurs here.
func (store *Store) DisposeRefund(ctx context.Context, input c.RefundSelection) error {
	if input.ActorAdminID == 0 || input.RefundID == 0 || input.OrderItemID == 0 || input.AccountID == 0 || input.ExpectedVersion == 0 {
		return c.ErrInvalid
	}
	input.SourceIDs = append([]uint{}, input.SourceIDs...)
	sort.Slice(input.SourceIDs, func(left, right int) bool { return input.SourceIDs[left] < input.SourceIDs[right] })
	err := store.ledgerTransaction(ctx, func(tx *gorm.DB) error {
		if err := lockRefundPurchase(tx, input.OrderItemID); err != nil {
			return err
		}
		// Event row serializes decisions per refund on both SQLite and PostgreSQL.
		if err := dblock.Row(tx.Model(&d.RefundNotice{}).Where("refund_id = ?", input.RefundID), "order_id", c.ErrNotFound); err != nil {
			return err
		}
		candidate, _, err := refundCandidate(tx, input.RefundID, input.OrderItemID)
		if err != nil {
			return err
		}
		if candidate.AccountID != input.AccountID || candidate.SnapshotHash != input.SnapshotHash {
			return c.ErrConflict
		}
		if candidate.Disposed {
			var audit d.RefundDisposition
			if err := tx.First(&audit, "refund_id = ? AND order_item_id = ?", input.RefundID, input.OrderItemID).Error; err != nil {
				return err
			}
			if input.ExpectedVersion != audit.ExpectedVersion || !reflect.DeepEqual(append([]uint{}, audit.SourceIDs...), input.SourceIDs) {
				return c.ErrConflict
			}
			return nil
		}
		if candidate.ExpectedVersion != input.ExpectedVersion || !reflect.DeepEqual(candidate.SourceIDs, input.SourceIDs) {
			return c.ErrConflict
		}
		advanced := tx.Model(&d.RefundNotice{}).Where("refund_id = ? AND version = ?", input.RefundID, input.ExpectedVersion).UpdateColumn("version", gorm.Expr("version + 1"))
		if advanced.Error != nil {
			return advanced.Error
		}
		if advanced.RowsAffected != 1 {
			return c.ErrConflict
		}
		bound, err := InTransaction(tx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		var snapshot d.OrderItemSnapshot
		if err := tx.First(&snapshot, "order_item_id = ?", input.OrderItemID).Error; err != nil {
			return err
		}
		sort.Slice(snapshot.Targets, func(left, right int) bool { return snapshot.Targets[left].TargetID < snapshot.Targets[right].TargetID })
		executions := []d.RefundExecution{}
		for _, target := range snapshot.Targets {
			key := d.SourceKey{EntitlementScope: d.EntitlementScope{TargetID: target.TargetID, BeneficiaryTelegramID: candidate.BeneficiaryTelegramID}, OrderItemID: input.OrderItemID}
			if err := bound.RevokeSource(ctx, key, now); err != nil {
				return err
			}
			targetExecutions, err := enqueueRefundExecutions(tx, target.TargetID, candidate.BeneficiaryTelegramID, now)
			if err != nil {
				return err
			}
			executions = append(executions, targetExecutions...)
		}
		return tx.Create(&d.RefundDisposition{RefundID: input.RefundID, OrderItemID: input.OrderItemID, ActorAdminID: input.ActorAdminID, AccountID: input.AccountID, BeneficiaryTelegramID: candidate.BeneficiaryTelegramID, SnapshotHash: input.SnapshotHash, SourceIDs: input.SourceIDs, ExpectedVersion: input.ExpectedVersion, ExecutionVersion: 1, Executions: executions, CreatedAt: now}).Error
	})
	slog.InfoContext(ctx, "membership_refund_disposition", "refund_id", input.RefundID, "order_item_id", input.OrderItemID, "committed", err == nil)
	return err
}

// paidFact 是支付/订单已有表的只读关联投影，不注册模型、不复制资金规则。
type paidFact struct {
	PaymentID uint
	OrderID   uint
	PaidAt    time.Time
}

// paidFacts 只选择已明确标记付款且无异常的成功支付；不依赖 source、联系人或当前商品。
// db 为本地句柄；返回可继续附加 ID 游标的查询。取消/不足额/异常永远不补事件。
func paidFacts(db *gorm.DB) *gorm.DB {
	return db.Table("payments AS p").Select("p.id AS payment_id, p.order_id, p.paid_at").
		Joins("JOIN orders AS o ON o.id = p.order_id").
		Where("p.deleted_at IS NULL AND o.deleted_at IS NULL AND p.status = ? AND p.paid_at IS NOT NULL", constants.PaymentStatusSuccess).
		Where("(p.exception_code IS NULL OR p.exception_code = '') AND o.paid_at IS NOT NULL AND o.canceled_at IS NULL").
		Where("o.status NOT IN ?", []string{constants.OrderStatusCanceled, constants.OrderStatusPendingPayment})
}

// hasPaidSnapshot 按真实 item.OrderID 与父单关系查询不可变快照，父单展示聚合不重复计数。
// db 为事务句柄，orderID 为付款订单；返回是否存在至少一个已付款真实项。
func hasPaidSnapshot(db *gorm.DB, orderID uint) (bool, error) {
	// 升级前普通商品 fixtures 不依赖会员 schema；生产必须先完成迁移再装配。
	if !db.Migrator().HasTable(&d.OrderItemSnapshot{}) {
		return false, nil
	}
	var count int64
	err := db.Table("telegram_membership_order_item_snapshots AS s").
		Joins("JOIN order_items AS i ON i.id = s.order_item_id").
		Joins("JOIN orders AS o ON o.id = i.order_id").
		Where("(o.id = ? OR o.parent_id = ?) AND o.paid_at IS NOT NULL AND o.canceled_at IS NULL", orderID, orderID).
		Where("i.deleted_at IS NULL AND o.deleted_at IS NULL AND o.status NOT IN ?", []string{constants.OrderStatusCanceled, constants.OrderStatusPendingPayment}).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// AppendPaid 在调用方事务内重读 orderID/paymentID 的可信事实，并幂等写入独立 outbox。
// 没有会员快照不读写事件表；失败原样返回，必须使外部支付事务回滚重试。
func (store *Store) AppendPaid(ctx context.Context, orderID, paymentID uint) error {
	if !store.boundTransaction {
		return c.ErrTransactionRequired
	}
	if orderID == 0 || paymentID == 0 {
		return c.ErrInvalid
	}
	db := store.db.WithContext(ctx)
	var fact paidFact
	if err := paidFacts(db).Where("p.id = ? AND p.order_id = ?", paymentID, orderID).Take(&fact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	has, err := hasPaidSnapshot(db, orderID)
	if err != nil || !has {
		return err
	}
	if fact.PaidAt.IsZero() {
		return c.ErrInvalid
	}
	event := d.PaidEvent{OrderID: orderID, PaymentID: paymentID, PaidAt: fact.PaidAt.UTC(), AvailableAt: time.Now().UTC()}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error; err != nil {
		return err
	}
	var existing d.PaidEvent
	if err := db.Where("order_id = ?", orderID).First(&existing).Error; err != nil {
		return err
	}
	if existing.PaymentID != paymentID || !existing.PaidAt.Equal(fact.PaidAt) {
		return c.ErrConflict
	}
	slog.InfoContext(ctx, "membership paid event staged", "order_id", orderID, "payment_id", paymentID)
	return nil
}

// RecoverPaid 从 afterPaymentID 之后分页扫描全部历史已付事实，不设最近 N 天窗口。
// limit 为每页上限；返回下一游标、扫描数量和错误。空页时调用方重置游标开始下一轮。
func (store *Store) RecoverPaid(ctx context.Context, afterPaymentID uint, limit int) (uint, int, error) {
	if limit < 1 || limit > 1000 {
		return afterPaymentID, 0, c.ErrInvalid
	}
	if !store.db.Migrator().HasTable(&d.OrderItemSnapshot{}) {
		return afterPaymentID, 0, nil
	}
	var facts []paidFact
	query := paidFacts(store.db.WithContext(ctx)).Where("p.id > ?", afterPaymentID).
		Where("EXISTS (SELECT 1 FROM telegram_membership_order_item_snapshots s JOIN order_items i ON i.id = s.order_item_id JOIN orders child ON child.id = i.order_id WHERE (child.id = p.order_id OR child.parent_id = p.order_id) AND i.deleted_at IS NULL AND child.deleted_at IS NULL AND child.paid_at IS NOT NULL)")
	if err := query.Order("p.id").Limit(limit).Find(&facts).Error; err != nil {
		return afterPaymentID, 0, err
	}
	next := afterPaymentID
	var failures error
	for _, fact := range facts {
		err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			bound, err := InTransaction(tx)
			if err != nil {
				return err
			}
			return bound.AppendPaid(ctx, fact.OrderID, fact.PaymentID)
		})
		if err != nil {
			failures = errors.Join(failures, err)
		}
		next = fact.PaymentID
	}
	return next, len(facts), failures
}

// ClaimPaid 按到期时间/ID 公平领取一条记录；owner 必须为每次领取新生成的随机值。
// now/ttl 仅控制工作租约；CAS 阻止并发领取，过期租约能自动恢复。
func (store *Store) ClaimPaid(ctx context.Context, owner string, now time.Time, ttl time.Duration) (*d.PaidEvent, error) {
	if owner == "" || len(owner) > 64 || now.IsZero() || ttl <= 0 {
		return nil, c.ErrInvalid
	}
	db := store.db.WithContext(ctx)
	var event d.PaidEvent
	err := db.Where("completed_at IS NULL AND available_at <= ? AND (claim_until IS NULL OR claim_until <= ?)", now, now).
		Order("available_at, id").First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	until := now.Add(ttl)
	// attempts/available_at 同时做 CAS，旧候选不能越过另一工作进程刚安排的退避。
	updated := db.Model(&d.PaidEvent{}).Where("id = ? AND completed_at IS NULL AND available_at <= ? AND attempts = ? AND (claim_until IS NULL OR claim_until <= ?)", event.ID, now, event.Attempts, now).
		Updates(map[string]any{"claim_owner": owner, "claim_until": until, "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, nil
	}
	event.ClaimOwner, event.ClaimUntil, event.Attempts = owner, &until, event.Attempts+1
	return &event, nil
}

// FinishPaid 仅允许 id 的当前 owner 在 now 租约尚有效时确认；失锁返回 ErrConflict。
func (store *Store) FinishPaid(ctx context.Context, id uint, owner string, now time.Time) error {
	return store.updatePaidLease(ctx, id, owner, now, map[string]any{"completed_at": now, "claim_until": nil, "claim_owner": "", "updated_at": now})
}

// RetryPaid 释放 id 的有效租约并安排 next；不存平台原始错误或任何敏感信息。
func (store *Store) RetryPaid(ctx context.Context, id uint, owner string, now, next time.Time) error {
	if !next.After(now) {
		return c.ErrInvalid
	}
	return store.updatePaidLease(ctx, id, owner, now, map[string]any{"available_at": next, "claim_until": nil, "claim_owner": "", "updated_at": now})
}

// updatePaidLease 为确认/重试提供同一 fence；参数为事件标识、owner、时钟及受控更新。
func (store *Store) updatePaidLease(ctx context.Context, id uint, owner string, now time.Time, values map[string]any) error {
	if id == 0 || owner == "" || now.IsZero() {
		return c.ErrInvalid
	}
	updated := store.db.WithContext(ctx).Model(&d.PaidEvent{}).
		Where("id = ? AND claim_owner = ? AND claim_until > ? AND completed_at IS NULL", id, owner, now).Updates(values)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return c.ErrConflict
	}
	return nil
}

// trustedLifecycleEligibility reuses historical source/route provenance for operation on tx.
// at fixes all-source calculation time; website rebinding never changes the task subject.
func trustedLifecycleEligibility(tx *gorm.DB, operation *d.AccessOperation, at time.Time) (c.AccessEligibility, error) {
	var member d.AccessMember
	err := tx.First(&member, "target_id = ? AND chat_id = ? AND telegram_id = ?", operation.TargetID, operation.ChatID, operation.TelegramID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && operation.LifecyclePhase == "check" {
		var invite d.AccessInvite
		err = tx.Where("target_id = ? AND chat_id = ? AND telegram_id = ? AND bot_id = ? AND route_version = ? AND source_id > 0", operation.TargetID, operation.ChatID, operation.TelegramID, operation.BotID, operation.RouteVersion).Order("id DESC").First(&invite).Error
		member = d.AccessMember{TargetID: invite.TargetID, TelegramID: invite.TelegramID, BotID: invite.BotID, RouteVersion: invite.RouteVersion, SourceID: invite.SourceID}
	}
	if err != nil {
		return c.AccessEligibility{}, err
	}
	if member.SourceID == 0 || member.BotID != operation.BotID || member.RouteVersion != operation.RouteVersion {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	matched, err := im.SourceMatches(tx, member.SourceID, member.TargetID, member.TelegramID)
	if err != nil {
		return c.AccessEligibility{}, err
	}
	if !matched {
		return c.AccessEligibility{}, c.ErrUntrustedIdentity
	}
	if _, err := lockProjection(tx, d.EntitlementScope{TargetID: operation.TargetID, BeneficiaryTelegramID: operation.TelegramID}); err != nil {
		return c.AccessEligibility{}, err
	}
	return accessEligibility(tx, c.AccessIdentity{TelegramID: operation.TelegramID}, operation.TargetID, at)
}

// recoverLifecycleEligibility updates retry scheduling values from trusted tx facts at at.
// Own-ban compensation and task-owned retired-route cleanup keep their original authorization.
func recoverLifecycleEligibility(tx *gorm.DB, operation *d.AccessOperation, values map[string]any, at time.Time) error {
	eligible, err := trustedLifecycleEligibility(tx, operation, at)
	if err != nil {
		return err
	}
	values["entitlement_version"] = eligible.Version
	if operation.BanConfirmed || strings.HasPrefix(operation.BusinessKey, "route-recovery:") {
		return nil
	}
	if eligible.Active {
		values["state"], values["lifecycle_phase"], values["review_reason"] = "succeeded", "done", "entitlement_active"
		return nil
	}
	pending, err := lifecyclePaidPending(tx, operation)
	if err != nil {
		return err
	}
	if pending {
		values["state"], values["review_reason"], values["next_attempt_at"] = "retryable", "payment_pending", at.Add(time.Minute)
	}
	return nil
}

// lifecyclePaidPending checks immutable snapshots with confirmed payment outbox work awaiting ledger posting.
// operation supplies trusted identity/target; migrated old identities cannot retain destination rights.
func lifecyclePaidPending(tx *gorm.DB, operation *d.AccessOperation) (bool, error) {
	mapping, err := im.Mapping(tx, operation.TelegramID, operation.TargetID)
	if err != nil {
		return false, err
	}
	identities := []int64{operation.TelegramID}
	if mapping != nil {
		if mapping.OldTelegramID == operation.TelegramID && mapping.State != "awaiting_member" {
			return false, nil
		}
		identities = []int64{mapping.OldTelegramID, mapping.NewTelegramID}
	}
	var snapshots []d.OrderItemSnapshot
	err = tx.Model(&d.OrderItemSnapshot{}).
		Joins("JOIN order_items item ON item.id = telegram_membership_order_item_snapshots.order_item_id").
		Joins("JOIN orders leaf ON leaf.id = item.order_id").
		Joins("JOIN telegram_membership_paid_events event ON event.order_id = COALESCE(leaf.parent_id, leaf.id)").
		Where("telegram_membership_order_item_snapshots.beneficiary_telegram_id IN ? AND item.deleted_at IS NULL AND leaf.deleted_at IS NULL", identities).
		Where("leaf.paid_at IS NOT NULL AND leaf.canceled_at IS NULL AND leaf.status <> ?", constants.OrderStatusPendingPayment).
		Where("event.completed_at IS NULL AND event.payment_id IN (?)", paidFacts(tx).Select("p.id").Where("p.order_id = event.order_id AND p.paid_at = event.paid_at")).Find(&snapshots).Error
	if err != nil {
		return false, err
	}
	for _, snapshot := range snapshots {
		for _, target := range snapshot.Targets {
			if target.TargetID != operation.TargetID {
				continue
			}
			var accounted int64
			if err := tx.Model(&d.LedgerSource{}).Where("order_item_id = ? AND target_id = ?", snapshot.OrderItemID, operation.TargetID).Count(&accounted).Error; err != nil {
				return false, err
			}
			if accounted > 0 {
				continue
			}
			if err := tx.Model(&d.LedgerRevocation{}).Where("order_item_id = ? AND target_id = ?", snapshot.OrderItemID, operation.TargetID).Count(&accounted).Error; err != nil {
				return false, err
			}
			if accounted == 0 {
				return true, nil
			}
		}
	}
	return false, nil
}
