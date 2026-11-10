package container

import (
	"context"
	"encoding/json"
	"fmt"
	order "github.com/XManCodeing/XMall/internal/modules/order/contract"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/workflows/membershiprefund"
	"gorm.io/gorm"
	"sync"
	"time"
)

// MembershipRefundWorkflow assembles the actual order money service and membership local disposition store.
// The permission adapter verifies a persisted backend admin, never a Telegram or website identity.
func (c *Container) MembershipRefundWorkflow() order.MembershipRefundAdmin {
	return &membershiprefund.Workflow{Funds: c.OrderRefundService, Rights: c.MembershipRefundAdmin, Authorize: c.authorizeMembershipRefund}
}

// authorizeMembershipRefund requires both money and entitlement permissions on every submission/retry.
// Existing-refund association still requires refund authority, despite not moving money again.
func (c *Container) authorizeMembershipRefund(ctx context.Context, actor uint, method string) error {
	path := "/admin/orders/:id/manual-refund"
	if method == "wallet" {
		path = "/admin/orders/:id/refund-to-wallet"
	}
	if method != "wallet" && method != "manual" && method != "existing" {
		return order.ErrRefundRequestInvalid
	}
	if err := c.authorizeSupportPermission(ctx, actor, path, "POST", true); err != nil {
		return fmt.Errorf("%w", order.ErrRefundRequestDenied)
	}
	if err := c.authorizeSupportPermission(ctx, actor, "/admin/telegram-bot/membership/refunds/dispose", "POST", true); err != nil {
		return fmt.Errorf("%w", order.ErrRefundRequestDenied)
	}
	return nil
}

// membershipRefundRecovery builds bounded, restart-safe rights-only compensation.
// db is initialized; durable request/disposition records provide multi-process idempotency.
func (c *Container) membershipRefundRecovery(db *gorm.DB) func(context.Context) error {
	var after uint
	var mutex sync.Mutex
	return func(ctx context.Context) error {
		mutex.Lock()
		defer mutex.Unlock()
		if !db.Migrator().HasTable(&order.MembershipRefundRequest{}) {
			return nil
		}
		workflow := &membershiprefund.Workflow{Funds: c.OrderRefundService, Rights: membershipstore.New(db)}
		read := func(ctx context.Context, cursor uint, limit int) ([]order.MembershipRefundRequest, error) {
			var requests []order.MembershipRefundRequest
			now := time.Now().UTC()
			err := db.WithContext(ctx).Where("id > ? AND refund_id > 0 AND funds_state = ? AND rights_state = ?", cursor, "completed", "pending").
				Where("retry_attempts < ? AND (retry_after IS NULL OR retry_after <= ?) AND (retry_lease_until IS NULL OR retry_lease_until <= ?)", order.MembershipRefundRetryLimit, now, now).
				Order("id").Limit(limit).Omit("ItemIDs").Find(&requests).Error
			if err != nil || len(requests) == 0 {
				return requests, err
			}
			// Decode each immutable selection separately: corrupt JSON must not poison the entire page.
			ids := make([]uint, 0, len(requests))
			for _, request := range requests {
				ids = append(ids, request.ID)
			}
			var selections []struct {
				ID      uint
				ItemIDs string
			}
			if err := db.WithContext(ctx).Table("membership_refund_requests").Select("id", "item_ids").Where("id IN ?", ids).Scan(&selections).Error; err != nil {
				return nil, err
			}
			byID := make(map[uint]string, len(selections))
			for _, selection := range selections {
				byID[selection.ID] = selection.ItemIDs
			}
			for index := range requests {
				if err := json.Unmarshal([]byte(byID[requests[index].ID]), &requests[index].ItemIDs); err != nil {
					// Nil selection is durably retried/quarantined by the same claimed workflow path.
					requests[index].ItemIDs = nil
				}
			}
			return requests, nil
		}
		next, scanned, err := workflow.Recover(ctx, read, after, 100)
		if scanned > 0 {
			after = next
		} else if err == nil {
			after = 0
		}
		return err
	}
}
