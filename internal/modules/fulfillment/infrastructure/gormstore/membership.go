package gormstore

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	fc "github.com/XManCodeing/XMall/internal/modules/fulfillment/contract"
	fd "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	oc "github.com/XManCodeing/XMall/internal/modules/order/contract"
	mc "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ fc.MembershipSystemStore = (*Store)(nil)
var _ fc.OrdinaryItemStore = (*Store)(nil)

// requireTransaction 拒绝普通连接；参数无，返回实际事务校验错误。
func (r *Store) requireTransaction() error {
	if r == nil || r.db == nil {
		return fc.ErrTransactionRequired
	}
	if _, ok := r.db.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return fc.ErrTransactionRequired
	}
	return nil
}

// CompleteMembership 在调用方账本事务中校验真实会员项并写可靠回执，返回任一失败。
// itemIDs 不允许跨付款根单；paidAt 必须与同事务已验证来源账本的可信付款时间一致。
func (r *Store) CompleteMembership(ctx context.Context, itemIDs []uint, paidAt time.Time) error {
	if err := r.requireTransaction(); err != nil {
		return err
	}
	if len(itemIDs) == 0 || paidAt.IsZero() || paidAt.After(time.Now()) {
		return fc.ErrMembershipInvalid
	}
	scoped := &Store{db: r.db.WithContext(ctx)}
	ids := append([]uint(nil), itemIDs...)
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	var rootID uint
	orders := map[uint]bool{}
	for index, id := range ids {
		if id == 0 {
			return fc.ErrMembershipInvalid
		}
		if index > 0 && id == ids[index-1] {
			continue
		}
		var item oc.FulfillmentItem
		if err := scoped.db.Where("id = ? AND deleted_at IS NULL", id).First(&item).Error; err != nil {
			return err
		}
		order, err := scoped.lockLeaf(item.OrderID, true)
		if err != nil {
			return err
		}
		paymentRoot := order.ID
		if order.ParentID != nil {
			paymentRoot = *order.ParentID
		}
		if rootID != 0 && rootID != paymentRoot {
			return fc.ErrMembershipInvalid
		}
		rootID = paymentRoot
		if !order.RefundedAmount.Decimal.IsZero() || order.Status == constants.OrderStatusRefunded || order.Status == constants.OrderStatusPartiallyRefunded {
			if !scoped.db.Migrator().HasTable("telegram_membership_ledger_sources") {
				return fc.ErrMembershipInvalid
			}
			var count int64
			if err := scoped.db.Table("telegram_membership_ledger_sources").Where("order_item_id = ? AND operation = ? AND paid_at = ?", id, "grant", paidAt).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return fc.ErrMembershipInvalid
			}
		}
		if err := scoped.validateGrantTime(id, paidAt, order.PaidAt); err != nil {
			return err
		}
		if err := scoped.validateSnapshot(order, &item); err != nil {
			return err
		}
		receipt := fd.ItemReceipt{OrderItemID: id, OrderID: order.ID, Kind: fd.SystemMembershipType, Status: fd.ReceiptEntitlementRecorded, PaidAt: paidAt, RecordedAt: time.Now().UTC()}
		if err := scoped.insertReceipt(receipt); err != nil {
			return err
		}
		orders[order.ID] = true
	}
	for orderID := range orders {
		// Financial refund state is preserved; recording entitlement does not rewrite it to delivered.
		var order oc.FulfillmentOrder
		if err := scoped.db.First(&order, orderID).Error; err != nil {
			return err
		}
		if !order.RefundedAmount.Decimal.IsZero() || order.Status == constants.OrderStatusRefunded || order.Status == constants.OrderStatusPartiallyRefunded {
			continue
		}
		if _, err := scoped.reconcile(orderID, time.Now().UTC(), true); err != nil {
			return err
		}
	}
	return nil
}

// validateGrantTime 使用同事务来源核对延迟回调的可信时间；orderPaid 是旧商城处理时点。
// itemID 必须已有账本来源。旧调用方时点一致时沿用原验证；不一致不能由调用参数单独放行。
func (r *Store) validateGrantTime(itemID uint, paidAt time.Time, orderPaid *time.Time) error {
	if orderPaid == nil {
		return fc.ErrMembershipInvalid
	}
	if orderPaid.Equal(paidAt) {
		return nil
	}
	var sources []struct{ PaidAt time.Time }
	if err := r.db.Table("telegram_membership_ledger_sources").Select("paid_at").Where("order_item_id = ? AND operation = ?", itemID, "grant").Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) == 0 {
		return fc.ErrMembershipInvalid
	}
	for _, source := range sources {
		if !source.PaidAt.Equal(paidAt) {
			return fc.ErrMembershipInvalid
		}
	}
	return nil
}

// lockLeaf locks root then leaf and validates payment/ownership; membershipOnly permits financial refunds only for ledger completion.
// OrdinaryItems never supplies membershipOnly, preserving ordinary merchandise refund policy.
func (r *Store) lockLeaf(orderID uint, membershipOnly ...bool) (*oc.FulfillmentOrder, error) {
	var order oc.FulfillmentOrder
	if err := r.db.Where("id = ? AND deleted_at IS NULL", orderID).First(&order).Error; err != nil {
		return nil, err
	}
	if order.ParentID != nil {
		var parent oc.FulfillmentOrder
		if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", *order.ParentID).First(&parent).Error; err != nil {
			return nil, err
		}
		if parent.ParentID != nil || parent.UserID != order.UserID || !paidOrder(&parent, membershipOnly...) {
			return nil, fc.ErrMembershipInvalid
		}
	}
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", orderID).First(&order).Error; err != nil {
		return nil, err
	}
	var children int64
	if err := r.db.Table("orders").Where("parent_id = ? AND deleted_at IS NULL", orderID).Count(&children).Error; err != nil {
		return nil, err
	}
	if children != 0 || !paidOrder(&order, membershipOnly...) {
		return nil, fc.ErrMembershipInvalid
	}
	return &order, nil
}

// paidOrder validates paid states and cancellation; membershipOnly never treats funds refunds as entitlement cancellation.
func paidOrder(order *oc.FulfillmentOrder, membershipOnly ...bool) bool {
	member := len(membershipOnly) > 0 && membershipOnly[0]
	if member && order.PaidAt == nil {
		return false
	}
	if order.CanceledAt != nil || !member && !order.RefundedAmount.Decimal.IsZero() {
		return false
	}
	if member && (order.Status == constants.OrderStatusRefunded || order.Status == constants.OrderStatusPartiallyRefunded) {
		return true
	}
	switch order.Status {
	case constants.OrderStatusPaid, constants.OrderStatusFulfilling, constants.OrderStatusDelivered, constants.OrderStatusCompleted, constants.OrderStatusPartiallyDelivered:
		return true
	}
	return false
}

// validateSnapshot 对照不可变快照与真实订单项，拒绝受益人、期限、目标或归属损坏。
func (r *Store) validateSnapshot(order *oc.FulfillmentOrder, item *oc.FulfillmentItem) error {
	var snapshot mc.OrderItemSnapshot
	if err := r.db.Table("telegram_membership_order_item_snapshots").Where("order_item_id = ?", item.ID).First(&snapshot).Error; err != nil {
		return err
	}
	if snapshot.ProductID != item.ProductID || snapshot.SKUID != item.SKUID || snapshot.AccountID != order.UserID || order.UserID == 0 || item.Quantity != 1 || snapshot.Quantity != 1 || snapshot.BeneficiaryTelegramID <= 0 || snapshot.IdentityVersion == 0 || snapshot.ConfigVersion == 0 || len(snapshot.Targets) == 0 {
		return fc.ErrMembershipInvalid
	}
	if snapshot.Kind == "permanent" {
		if snapshot.Months != 0 || snapshot.DurationSeconds != 0 {
			return fc.ErrMembershipInvalid
		}
	} else if snapshot.Kind != "months" || snapshot.Months <= 0 || snapshot.Months > math.MaxInt64/int64(time.Second)/2592000 || snapshot.DurationSeconds != snapshot.Months*2592000 {
		return fc.ErrMembershipInvalid
	}
	targets := map[uint]bool{}
	for _, target := range snapshot.Targets {
		if target.TargetID == 0 || targets[target.TargetID] || target.RouteVersion == 0 || target.BotInstanceID == 0 || target.TelegramChatID >= 0 {
			return fc.ErrMembershipInvalid
		}
		targets[target.TargetID] = true
	}
	return nil
}

// insertReceipt 写不可变唯一回执；重放必须与已存在的业务事实完全一致。
func (r *Store) insertReceipt(receipt fd.ItemReceipt) error {
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var existing fd.ItemReceipt
	if err := r.db.First(&existing, "order_item_id = ?", receipt.OrderItemID).Error; err != nil {
		return err
	}
	if existing.OrderID != receipt.OrderID || existing.Kind != receipt.Kind || existing.Status != receipt.Status || !existing.PaidAt.Equal(receipt.PaidAt) {
		return fc.ErrMembershipInvalid
	}
	return nil
}

// membershipIDs 返回订单会员快照集合；旧的非会员独立测试数据库可没有会员表。
func (r *Store) membershipIDs(orderID uint) (map[uint]bool, error) {
	ids := map[uint]bool{}
	if !r.db.Migrator().HasTable("telegram_membership_order_item_snapshots") {
		return ids, nil
	}
	var rows []uint
	err := r.db.Table("telegram_membership_order_item_snapshots AS snapshots").Joins("JOIN order_items ON order_items.id = snapshots.order_item_id").Where("order_items.order_id = ? AND order_items.deleted_at IS NULL", orderID).Pluck("snapshots.order_item_id", &rows).Error
	for _, id := range rows {
		ids[id] = true
	}
	return ids, err
}

// OrdinaryItems 返回指定 kind 的普通项；必须在事务中锁单，会员项永远排除。
func (r *Store) OrdinaryItems(orderID uint, kind string) ([]uint, error) {
	if err := r.requireTransaction(); err != nil {
		return nil, err
	}
	if _, err := r.lockLeaf(orderID); err != nil {
		return nil, err
	}
	members, err := r.membershipIDs(orderID)
	if err != nil {
		return nil, err
	}
	var items []oc.FulfillmentItem
	if err := r.db.Where("order_id = ? AND deleted_at IS NULL", orderID).Find(&items).Error; err != nil {
		return nil, err
	}
	ids := []uint{}
	if r.db.Migrator().HasTable(&fd.ItemReceipt{}) {
		var completed int64
		if err := r.db.Model(&fd.ItemReceipt{}).Where("order_id = ? AND kind = ?", orderID, kind).Count(&completed).Error; err != nil {
			return nil, err
		}
		if completed > 0 {
			return nil, fc.ErrMembershipInvalid
		}
	}
	for _, item := range items {
		if !members[item.ID] && r.db.Migrator().HasTable("telegram_membership_product_configs") {
			var configured int64
			if err := r.db.Table("telegram_membership_product_configs").Where("product_id = ?", item.ProductID).Count(&configured).Error; err != nil {
				return nil, err
			}
			if configured > 0 {
				return nil, fc.ErrMembershipInvalid
			}
		}
		if !members[item.ID] && (item.FulfillmentType == kind || (kind == constants.FulfillmentTypeManual && item.FulfillmentType == "")) {
			ids = append(ids, item.ID)
		}
	}
	// Legacy manual orders without item rows retain their historical behavior.
	if len(ids) == 0 && len(items) > 0 {
		return nil, fc.ErrMembershipInvalid
	}
	return ids, nil
}

// CompleteOrdinary 将真实交付绑定普通项；会员项尚无回执时仅保留 fulfilling 状态。
func (r *Store) CompleteOrdinary(fulfillment *fd.Fulfillment) (bool, error) {
	ids, err := r.OrdinaryItems(fulfillment.OrderID, fulfillment.Type)
	if err != nil {
		return false, err
	}
	var order oc.FulfillmentOrder
	if err := r.db.First(&order, fulfillment.OrderID).Error; err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if !r.db.Migrator().HasTable(&fd.ItemReceipt{}) {
		members, err := r.membershipIDs(order.ID)
		if err != nil {
			return false, err
		}
		if len(members) > 0 {
			return false, fc.ErrMembershipInvalid
		}
		status := constants.OrderStatusDelivered
		if fulfillment.Type == constants.FulfillmentTypeAuto {
			status = constants.OrderStatusCompleted
		}
		return true, r.updateStatus(&order, status, now)
	}
	paidAt := now
	if order.PaidAt != nil {
		paidAt = *order.PaidAt
	}
	for _, id := range ids {
		if err := r.insertReceipt(fd.ItemReceipt{OrderItemID: id, OrderID: order.ID, Kind: fulfillment.Type, Status: fd.ReceiptDelivered, FulfillmentID: &fulfillment.ID, PaidAt: paidAt, RecordedAt: now}); err != nil {
			return false, err
		}
	}
	return r.reconcile(order.ID, now)
}

// lookupFulfillment 是内部非报错缺省读取；仅返回未软删除记录。
func (r *Store) lookupFulfillment(orderID uint) (*fd.Fulfillment, error) {
	fulfillment, err := r.GetByOrderID(orderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return fulfillment, err
}
