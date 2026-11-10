package gormstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/persistence/gormutil"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ClaimMembershipRefund atomically fences a due request before any local side effect.
// snapshot is the scanned version; manual can resume exhausted intent but cannot steal a live lease.
// The provisional schedule and budget are committed first, retaining safety if Finish fails or the process dies.
func (store *Store) ClaimMembershipRefund(ctx context.Context, snapshot ordercontract.MembershipRefundRequest, now time.Time, manual bool) (*ordercontract.MembershipRefundRequest, error) {
	if snapshot.ID == 0 {
		return nil, ordercontract.ErrRefundRequestInvalid
	}
	query := store.db.WithContext(ctx).Model(&orderdomain.MembershipRefundRequest{}).
		Where("id = ? AND request_key = ? AND retry_version = ? AND refund_id > 0 AND funds_state = ?", snapshot.ID, snapshot.RequestKey, snapshot.RetryVersion, "completed").
		Where("retry_lease_until IS NULL OR retry_lease_until <= ?", now)
	attempt := snapshot.RetryAttempts + 1
	if manual {
		query = query.Where("rights_state = ?", "pending")
		attempt = 1
	} else {
		query = query.Where("rights_state = ? AND retry_attempts < ?", "pending", ordercontract.MembershipRefundRetryLimit).
			Where("retry_after IS NULL OR retry_after <= ?", now)
	}
	// Keep the existing pending business state so administrator recovery remains discoverable.
	// RetryAttempts, not a new rights state, excludes exhausted requests from automatic work.
	state := "pending"
	lease := now.Add(ordercontract.MembershipRefundRetryLease)
	next := lease.Add(ordercontract.MembershipRefundRetryDelay(attempt))
	result := query.Updates(map[string]any{"retry_attempts": attempt, "retry_version": snapshot.RetryVersion + 1,
		"retry_lease_until": lease, "retry_after": next, "rights_state": state, "updated_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	snapshot.RetryAttempts, snapshot.RetryVersion = attempt, snapshot.RetryVersion+1
	snapshot.RetryLeaseUntil, snapshot.RetryAfter, snapshot.RightsState = &lease, &next, state
	return &snapshot, nil
}

// FinishMembershipRefund persists success or backoff only for the current unexpired fenced claim.
// success means local rights are queued; failures retain all monetary and termination evidence.
func (store *Store) FinishMembershipRefund(ctx context.Context, claim ordercontract.MembershipRefundRequest, now time.Time, success bool) error {
	state := "pending"
	var next *time.Time
	if success {
		state = "queued"
	} else if claim.RetryAttempts < ordercontract.MembershipRefundRetryLimit {
		due := now.Add(ordercontract.MembershipRefundRetryDelay(claim.RetryAttempts))
		next = &due
	}
	result := store.db.WithContext(ctx).Model(&orderdomain.MembershipRefundRequest{}).
		Where("id = ? AND retry_version = ? AND retry_lease_until > ?", claim.ID, claim.RetryVersion, now).
		Updates(map[string]any{"rights_state": state, "retry_after": next, "retry_lease_until": nil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ordercontract.ErrRefundRequestConflict
	}
	return nil
}

// BeginMembershipRefund validates explicit purchase ownership and inserts the command inside the money transaction.
// A replay aborts before any monetary mutation; the caller then reads the committed command.
func (store *Store) BeginMembershipRefund(request *ordercontract.MembershipRefundRequest) error {
	if request == nil || request.RequestKey == "" || len(request.ItemIDs) == 0 {
		return ordercontract.ErrRefundRequestInvalid
	}
	result := store.db.Clauses(clause.OnConflict{DoNothing: true}).Create(request)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		previous, err := store.GetMembershipRefund(request.RequestKey)
		if err != nil {
			return err
		}
		if previous == nil || previous.Fingerprint != request.Fingerprint {
			return ordercontract.ErrRefundRequestConflict
		}
		return ordercontract.ErrRefundRequestReplay
	}
	var order orderdomain.Order
	if err := store.db.Where("deleted_at IS NULL").First(&order, request.OrderID).Error; err != nil {
		return err
	}
	if order.UserID == 0 || order.PaidAt == nil {
		return ordercontract.ErrRefundRequestInvalid
	}
	if err := store.preventDuplicateMembershipTermination(order, request); err != nil {
		return err
	}
	for _, itemID := range request.ItemIDs {
		if err := store.validateMembershipRefundItem(order, itemID); err != nil {
			return err
		}
	}
	return nil
}

// preventDuplicateMembershipTermination rejects a second new-money command for already selected purchases.
// Root locking serializes parent/child requests; existing-refund repair does not move money.
func (store *Store) preventDuplicateMembershipTermination(order orderdomain.Order, request *ordercontract.MembershipRefundRequest) error {
	if request.Method == "existing" {
		return nil
	}
	root := order.ID
	if order.ParentID != nil {
		root = *order.ParentID
	}
	var previous []orderdomain.MembershipRefundRequest
	if err := store.db.Where("id <> ? AND refund_id > 0 AND order_id IN (SELECT id FROM orders WHERE id = ? OR parent_id = ?)", request.ID, root, root).Find(&previous).Error; err != nil {
		return err
	}
	selected := map[uint]bool{}
	for _, id := range request.ItemIDs {
		selected[id] = true
	}
	for _, command := range previous {
		for _, id := range command.ItemIDs {
			if selected[id] {
				return ordercontract.ErrRefundRequestConflict
			}
		}
	}
	return nil
}

// validateMembershipRefundItem uses transaction-local immutable snapshots and actual parent/child ownership.
// Cross-account, sibling, deleted, unpaid and ordinary product selections return errors.
func (store *Store) validateMembershipRefundItem(order orderdomain.Order, itemID uint) error {
	var item orderdomain.OrderItem
	if err := store.db.Where("deleted_at IS NULL").First(&item, itemID).Error; err != nil {
		return err
	}
	var leaf orderdomain.Order
	if err := store.db.Where("deleted_at IS NULL").First(&leaf, item.OrderID).Error; err != nil {
		return err
	}
	if leaf.UserID != order.UserID || leaf.PaidAt == nil || (leaf.ID != order.ID && (leaf.ParentID == nil || *leaf.ParentID != order.ID)) {
		return ordercontract.ErrRefundRequestInvalid
	}
	var snapshot membership.OrderItemSnapshot
	if err := store.db.Table("telegram_membership_order_item_snapshots").Where("order_item_id = ?", itemID).Take(&snapshot).Error; err != nil {
		return err
	}
	if snapshot.AccountID != order.UserID || snapshot.ProductID != item.ProductID || snapshot.SKUID != item.SKUID || snapshot.BeneficiaryTelegramID <= 0 || len(snapshot.Targets) == 0 {
		return ordercontract.ErrRefundRequestInvalid
	}
	return nil
}

// BindMembershipRefund verifies a genuine settled refund and binds intent before money transaction commit.
func (store *Store) BindMembershipRefund(key string, refundID uint) error {
	request, err := store.GetMembershipRefund(key)
	if err != nil {
		return err
	}
	if request == nil || request.RefundID != 0 || refundID == 0 {
		return ordercontract.ErrRefundRequestInvalid
	}
	record, err := store.GetRefundRecordByIDForUpdate(refundID)
	if err != nil {
		return err
	}
	if record == nil || record.OrderID != request.OrderID || record.UserID == 0 || !record.Amount.Decimal.IsPositive() || (record.Type != constants.OrderRefundTypeManual && record.Type != constants.OrderRefundTypeWallet) {
		return ordercontract.ErrRefundRequestInvalid
	}
	order, err := store.GetByIDForUpdate(request.OrderID)
	if err != nil {
		return err
	}
	if order == nil || order.UserID != record.UserID || order.PaidAt == nil || order.RefundedAmount.Decimal.LessThan(record.Amount.Decimal) {
		return ordercontract.ErrRefundRequestInvalid
	}
	result := store.db.Model(&orderdomain.MembershipRefundRequest{}).Where("request_key = ? AND refund_id = 0", key).Updates(map[string]any{"refund_id": refundID, "funds_state": "completed", "rights_state": "pending", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ordercontract.ErrRefundRequestConflict
	}
	return nil
}

// GetMembershipRefund returns persisted idempotent intent, or nil if absent.
func (store *Store) GetMembershipRefund(key string) (*ordercontract.MembershipRefundRequest, error) {
	var request ordercontract.MembershipRefundRequest
	err := store.db.Where("request_key = ?", key).Take(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &request, err
}

// ListMembershipRefunds exposes only actor-owned recovery requests after authorization.
func (store *Store) ListMembershipRefunds(orderID, actor uint) ([]ordercontract.MembershipRefundRequest, error) {
	rows := []ordercontract.MembershipRefundRequest{}
	err := store.db.Where("order_id = ? AND actor_admin_id = ?", orderID, actor).Order("id DESC").Limit(100).Find(&rows).Error
	return rows, err
}

// CompleteMembershipRefund marks local rights complete; external Telegram cleanup remains separately queued.
func (store *Store) CompleteMembershipRefund(key string) error {
	result := store.db.Model(&orderdomain.MembershipRefundRequest{}).Where("request_key = ? AND refund_id > 0", key).Updates(map[string]any{"rights_state": "queued", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ordercontract.ErrRefundRequestInvalid
	}
	return nil
}

// LockRefundScope uses the same root -> child -> procurement order as submission.
// It is invoked only through a transaction-bound Store.
func (r *Store) LockRefundScope(id uint) error {
	if r.db.Dialector.Name() == "sqlite" {
		if err := r.db.Exec("UPDATE orders SET status = status WHERE id = ?", id).Error; err != nil {
			return err
		}
	}
	var target orderdomain.Order
	if err := r.db.Select("id", "parent_id").First(&target, id).Error; err != nil {
		return err
	}
	rootID := id
	if target.ParentID != nil {
		rootID = *target.ParentID
	}
	var root orderdomain.Order
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&root, rootID).Error; err != nil {
		return err
	}
	var children []orderdomain.Order
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("parent_id = ? AND deleted_at IS NULL", rootID).Order("id").Find(&children).Error; err != nil {
		return err
	}
	ids := []uint{id}
	if target.ParentID == nil {
		for _, child := range children {
			ids = append(ids, child.ID)
		}
	}
	// Some isolated legacy schema tests omit procurement entirely.
	if !r.db.Migrator().HasTable("procurement_orders") {
		return nil
	}
	var count int64
	if err := r.db.Table("procurement_orders AS p").Where("p.local_order_id IN ? AND p.deleted_at IS NULL AND p.status IN ?", ids, []string{"submitting", "unknown", "accepted", "partially_refunded"}).
		Where("NOT EXISTS (SELECT 1 FROM fulfillments f WHERE f.order_id = p.local_order_id AND f.deleted_at IS NULL AND f.status = ? AND f.payload <> '')", "delivered").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ordercontract.ErrProcurementRefundBlocked
	}
	return nil
}

// Create 创建退款记录
func (r *Store) CreateRefundRecord(record *orderdomain.OrderRefundRecord) error {
	if record == nil {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		// Legacy isolated order schemas have no membership module; production migrates v18 first.
		if !tx.Migrator().HasTable("telegram_membership_order_item_snapshots") {
			return nil
		}
		var count int64
		if err := tx.Table("telegram_membership_order_item_snapshots AS snapshot").
			Joins("JOIN order_items item ON item.id = snapshot.order_item_id").
			Joins("JOIN orders child ON child.id = item.order_id").
			Where("(child.id = ? OR child.parent_id = ?) AND child.deleted_at IS NULL AND item.deleted_at IS NULL", record.OrderID, record.OrderID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&membership.RefundNotice{RefundID: record.ID, OrderID: record.OrderID, CreatedAt: record.CreatedAt}).Error
	})
}

// GetByID 根据 ID 获取退款记录
func (r *Store) GetRefundRecordByID(id uint) (*orderdomain.OrderRefundRecord, error) {
	if id == 0 {
		return nil, nil
	}
	var record orderdomain.OrderRefundRecord
	if err := r.db.Where("deleted_at IS NULL").First(&record, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

func (r *Store) GetRefundRecordByIDForUpdate(id uint) (*orderdomain.OrderRefundRecord, error) {
	if id == 0 {
		return nil, nil
	}
	var record orderdomain.OrderRefundRecord
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("deleted_at IS NULL").
		First(&record, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &record, nil
}

func (r *Store) UpdateRefundRecordPaymentFee(id uint, refunded bool, amount money.Amount, updatedAt time.Time) error {
	return r.db.Model(&orderdomain.OrderRefundRecord{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]interface{}{
			"payment_fee_refunded":        refunded,
			"payment_fee_refunded_amount": amount,
			"updated_at":                  updatedAt,
		}).Error
}

// ListByOrderIDs 按订单ID列表获取退款记录（按创建时间倒序）
func (r *Store) ListRefundRecordsByOrderIDs(orderIDs []uint) ([]orderdomain.OrderRefundRecord, error) {
	records := make([]orderdomain.OrderRefundRecord, 0)
	if len(orderIDs) == 0 {
		return records, nil
	}
	if err := r.db.
		Where("deleted_at IS NULL AND order_id IN ?", orderIDs).
		Order("created_at DESC, id DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListAdmin 管理端退款记录列表
func (r *Store) ListRefundRecordsAdmin(filter ordercontract.RefundRecordListFilter) ([]orderdomain.OrderRefundRecord, int64, error) {
	records := make([]orderdomain.OrderRefundRecord, 0)
	query := r.db.Model(&orderdomain.OrderRefundRecord{}).
		Joins("LEFT JOIN orders ON orders.id = order_refund_records.order_id AND orders.deleted_at IS NULL").
		Where("order_refund_records.deleted_at IS NULL")

	if filter.UserID != 0 {
		query = query.Where("orders.user_id = ?", filter.UserID)
	}
	if keyword := strings.TrimSpace(filter.UserKeyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where(
			"orders.user_id IN ("+
				"SELECT users.id FROM users "+
				"WHERE users.deleted_at IS NULL AND ("+
				"users.email LIKE ? OR "+
				"users.display_name LIKE ? OR "+
				"EXISTS ("+
				"SELECT 1 FROM user_oauth_identities "+
				"WHERE user_oauth_identities.user_id = users.id AND ("+
				"user_oauth_identities.provider LIKE ? OR "+
				"user_oauth_identities.provider_user_id LIKE ? OR "+
				"user_oauth_identities.username LIKE ?"+
				")"+
				")"+
				")"+
				")",
			like, like, like, like, like,
		)
	}
	if orderNo := strings.TrimSpace(filter.OrderNo); orderNo != "" {
		query = query.Where("orders.order_no = ?", orderNo)
	}
	if guestEmail := strings.TrimSpace(filter.GuestEmail); guestEmail != "" {
		query = query.Where("(order_refund_records.guest_email = ? OR orders.guest_email = ?)", guestEmail, guestEmail)
	}
	if keyword := strings.TrimSpace(filter.ProductKeyword); keyword != "" {
		like := "%" + keyword + "%"
		cond, argCount := gormutil.BuildLocalizedLikeCondition(r.db, nil, []string{"oi.title_json"})
		if cond != "" {
			args := gormutil.RepeatLikeArgs(like, argCount)
			query = query.Where(
				"EXISTS (SELECT 1 FROM order_items oi WHERE oi.deleted_at IS NULL AND oi.order_id = order_refund_records.order_id AND ("+cond+"))",
				args...,
			)
		}
	}
	if filter.CreatedFrom != nil {
		query = query.Where("order_refund_records.created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("order_refund_records.created_at <= ?", *filter.CreatedTo)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	dataQuery := gormutil.ApplyPagination(query.Session(&gorm.Session{}), filter.Page, filter.PageSize)
	if err := dataQuery.
		Order("order_refund_records.id DESC").
		Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}
