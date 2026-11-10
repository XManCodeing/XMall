package gormstore

import (
	"context"
	"log/slog"
	"sort"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"gorm.io/gorm"
)

// OrderMembershipHistory reads only account/beneficiary-owned order snapshots using one
// transaction connection. It returns deduplicated historical routes without reading global
// target history, requesting invitations, or modifying immutable purchase provenance.
func (store *Store) OrderMembershipHistory(ctx context.Context, account uint, beneficiary int64, order uint) ([]c.OrderMembershipTarget, error) {
	if account == 0 || beneficiary <= 0 || order == 0 {
		return nil, c.ErrInvalid
	}
	var snapshots []d.OrderItemSnapshot
	err := store.transaction(ctx, func(tx *gorm.DB) error {
		current, err := userAccessBinding(tx, account)
		if err != nil {
			return err
		}
		if current != beneficiary {
			return c.ErrUntrustedIdentity
		}
		var owned int64
		if err := tx.Table("orders").Where("id = ? AND user_id = ? AND deleted_at IS NULL", order, account).Count(&owned).Error; err != nil {
			return err
		}
		if owned != 1 {
			return c.ErrUntrustedIdentity
		}
		return tx.Table("telegram_membership_order_item_snapshots AS snapshot").Select("snapshot.*").
			Joins("JOIN order_items AS item ON item.id = snapshot.order_item_id").
			Joins("JOIN orders AS purchase ON purchase.id = item.order_id").
			Where("(purchase.id = ? OR purchase.parent_id = ?) AND purchase.user_id = ? AND snapshot.account_id = ?", order, order, account, account).
			Where("snapshot.beneficiary_telegram_id = ? AND item.deleted_at IS NULL AND purchase.deleted_at IS NULL", beneficiary).
			Where("item.product_id = snapshot.product_id AND item.sku_id = snapshot.sku_id AND item.quantity = 1 AND snapshot.quantity = 1").
			Order("snapshot.order_item_id").Find(&snapshots).Error
	})
	if err != nil {
		slog.WarnContext(ctx, "membership order history projection rejected", "order_id", order)
		return nil, err
	}
	return orderMembershipHistory(snapshots), nil
}

// orderMembershipHistory groups supplied immutable snapshots by logical target and
// deduplicates exact physical routes; stable ordering makes repeated reads deterministic.
func orderMembershipHistory(snapshots []d.OrderItemSnapshot) []c.OrderMembershipTarget {
	grouped := make(map[uint][]c.OrderMembershipRoute)
	seen := make(map[uint]map[c.OrderMembershipRoute]bool)
	for _, snapshot := range snapshots {
		for _, target := range snapshot.Targets {
			if target.TargetID == 0 {
				continue
			}
			if seen[target.TargetID] == nil {
				seen[target.TargetID] = make(map[c.OrderMembershipRoute]bool)
			}
			route := c.OrderMembershipRoute{BotInstanceID: target.BotInstanceID, ChatID: target.TelegramChatID, RouteVersion: target.RouteVersion}
			if !seen[target.TargetID][route] {
				seen[target.TargetID][route] = true
				grouped[target.TargetID] = append(grouped[target.TargetID], route)
			}
		}
	}
	out := make([]c.OrderMembershipTarget, 0, len(grouped))
	for target, routes := range grouped {
		out = append(out, c.OrderMembershipTarget{TargetID: target, Routes: routes})
	}
	sort.Slice(out, func(left, right int) bool { return out[left].TargetID < out[right].TargetID })
	return out
}

var _ c.OrderMembershipHistoryReader = (*Store)(nil)
