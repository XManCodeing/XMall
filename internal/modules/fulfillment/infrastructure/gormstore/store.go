package gormstore

import (
	"errors"
	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fd "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	"strings"
	"time"

	fulfillmentcontract "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 是交付记录端口的 GORM 实现。
type Store struct{ db *gorm.DB }

var _ fulfillmentcontract.Store = (*Store)(nil)

// New 将 db 绑定为履约存储；系统履约调用时 db 必须为实际开启的事务句柄。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Create 创建交付记录
func (r *Store) Create(fulfillment *fulfillmentdomain.Fulfillment) error {
	return r.db.Create(fulfillment).Error
}

// GetByOrderID 根据订单 ID 获取交付记录(不存在返回 nil, nil),不加锁。
func (r *Store) GetByOrderID(orderID uint) (*fulfillmentdomain.Fulfillment, error) {
	var existing fulfillmentdomain.Fulfillment
	if err := r.db.Where("deleted_at IS NULL AND order_id = ?", orderID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &existing, nil
}

// FindByOrderIDForUpdate 用于事务内的存在性检查,加 SELECT ... FOR UPDATE 行锁防止并发双重交付。
// 返回 (record, found, err)。
func (r *Store) FindByOrderIDForUpdate(orderID uint) (*fulfillmentdomain.Fulfillment, bool, error) {
	var existing fulfillmentdomain.Fulfillment
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("deleted_at IS NULL AND order_id = ?", orderID).
		First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &existing, true, nil
}

// reconcile 仅当叶单所有存活项有匹配回执才完成，返回叶单是否完成及数据库错误。
func (r *Store) reconcile(orderID uint, now time.Time, membershipOnly ...bool) (bool, error) {
	order, err := r.lockLeaf(orderID, membershipOnly...)
	if err != nil {
		return false, err
	}
	var items []oc.FulfillmentItem
	if err := r.db.Where("order_id = ? AND deleted_at IS NULL", orderID).Find(&items).Error; err != nil {
		return false, err
	}
	members, err := r.membershipIDs(orderID)
	if err != nil {
		return false, err
	}
	var receipts []fd.ItemReceipt
	if err := r.db.Where("order_id = ?", orderID).Find(&receipts).Error; err != nil {
		return false, err
	}
	byItem := map[uint]fd.ItemReceipt{}
	for _, receipt := range receipts {
		byItem[receipt.OrderItemID] = receipt
	}
	complete, allAuto := len(items) > 0, len(items) > 0
	for _, item := range items {
		receipt, ok := byItem[item.ID]
		if members[item.ID] {
			allAuto = false
			if !ok || receipt.Kind != fd.SystemMembershipType || receipt.Status != fd.ReceiptEntitlementRecorded {
				complete = false
			}
		} else {
			if item.FulfillmentType != constants.FulfillmentTypeAuto {
				allAuto = false
			}
			if !ok || receipt.Status != fd.ReceiptDelivered || receipt.Kind == fd.SystemMembershipType {
				complete = false
			}
		}
	}
	status := constants.OrderStatusFulfilling
	if complete {
		status = constants.OrderStatusDelivered
		if allAuto {
			status = constants.OrderStatusCompleted
		}
	}
	existing, err := r.lookupFulfillment(orderID)
	if err != nil {
		return false, err
	}
	if complete && existing == nil {
		fulfillment := fd.Fulfillment{OrderID: orderID, Type: fd.SystemMembershipType, Status: fd.ReceiptEntitlementRecorded, DeliveredAt: &now, CreatedAt: now, UpdatedAt: now}
		if len(members) != len(items) {
			return false, fc.ErrMembershipInvalid
		}
		if err := r.db.Create(&fulfillment).Error; err != nil {
			return false, err
		}
	}
	if err := r.updateStatus(order, status, now); err != nil {
		return false, err
	}
	return complete, nil
}

// updateStatus 原子更新叶单与付款父单；仅聚合履约状态，不覆盖取消或退款事实。
func (r *Store) updateStatus(order *oc.FulfillmentOrder, status string, now time.Time) error {
	if err := r.db.Table("orders").Where("id = ?", order.ID).Updates(map[string]interface{}{"status": status, "updated_at": now}).Error; err != nil {
		return err
	}
	if order.ParentID == nil {
		return nil
	}
	var children []oc.FulfillmentOrder
	if err := r.db.Where("parent_id = ? AND deleted_at IS NULL", *order.ParentID).Find(&children).Error; err != nil {
		return err
	}
	completed, delivered, fulfilling := 0, 0, 0
	for _, child := range children {
		switch child.Status {
		case constants.OrderStatusCompleted:
			completed++
		case constants.OrderStatusDelivered:
			delivered++
		case constants.OrderStatusFulfilling:
			fulfilling++
		case constants.OrderStatusCanceled, constants.OrderStatusRefunded, constants.OrderStatusPartiallyRefunded:
			return nil
		}
	}
	parentStatus := constants.OrderStatusPaid
	switch {
	case len(children) > 0 && completed == len(children):
		parentStatus = constants.OrderStatusCompleted
	case len(children) > 0 && completed+delivered == len(children):
		parentStatus = constants.OrderStatusDelivered
	case completed+delivered > 0:
		parentStatus = constants.OrderStatusPartiallyDelivered
	case fulfilling > 0:
		parentStatus = constants.OrderStatusFulfilling
	}
	return r.db.Table("orders").Where("id = ? AND canceled_at IS NULL AND status NOT IN ?", *order.ParentID, []string{constants.OrderStatusCanceled, constants.OrderStatusRefunded, constants.OrderStatusPartiallyRefunded}).Updates(map[string]interface{}{"status": parentStatus, "updated_at": now}).Error
}

// SaveOrdinary 保留同叶单既有交付内容；输入是本次真实交付，返回数据库错误。
// 同类型重复由普通项回执拒绝，系统会员记录不占用普通交付的先后顺序。
func (r *Store) SaveOrdinary(fulfillment *fd.Fulfillment) error {
	if err := r.requireTransaction(); err != nil {
		return err
	}
	existing, err := r.lookupFulfillment(fulfillment.OrderID)
	if err != nil {
		return err
	}
	if existing == nil {
		return r.db.Create(fulfillment).Error
	}
	if !r.db.Migrator().HasTable(&fd.ItemReceipt{}) {
		return errors.New("fulfillment already exists")
	}
	var count int64
	if err := r.db.Model(&fd.ItemReceipt{}).Where("order_id = ? AND kind = ?", fulfillment.OrderID, fulfillment.Type).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fc.ErrMembershipInvalid
	}
	fulfillment.ID = existing.ID
	payload := strings.TrimSpace(existing.Payload + "\n" + fulfillment.Payload)
	updates := map[string]interface{}{"payload": payload, "status": fulfillment.Status, "updated_at": fulfillment.UpdatedAt, "delivered_at": fulfillment.DeliveredAt}
	if fulfillment.DeliveredBy != nil {
		updates["delivered_by"] = fulfillment.DeliveredBy
		updates["logistics_json"] = fulfillment.LogisticsJSON
	}
	if existing.Type == fd.SystemMembershipType {
		updates["type"] = fulfillment.Type
	}
	if err := r.db.Model(&fd.Fulfillment{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		return err
	}
	fulfillment.Payload = payload
	return nil
}
