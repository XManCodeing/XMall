package gormstore

import (
	"errors"
	"strings"
	"time"

	ordercontract "github.com/homles698698-lab/XMall/internal/modules/order/contract"
	orderdomain "github.com/homles698698-lab/XMall/internal/modules/order/domain"
	"github.com/homles698698-lab/XMall/internal/persistence/gormutil"
	"github.com/homles698698-lab/XMall/internal/shared/money"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	return r.db.Create(record).Error
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
