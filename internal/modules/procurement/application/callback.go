package application

import (
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	procurementcontract "github.com/dujiao-next/internal/modules/procurement/contract"
)

// HandleUpstreamCallback 处理上游回调通知
func (s *Service) HandleUpstreamCallback(procurementOrderID uint, upstreamStatus string, fulfillment *procurementcontract.Fulfillment) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return procurementcontract.ErrNotFound
	}

	now := time.Now()
	upstreamStatus = strings.ToLower(strings.TrimSpace(upstreamStatus))

	// 状态机守卫：已进入终态的采购单不再接受交付 / 取消回调
	if !isUpstreamTransitionAllowed(procOrder.Status, upstreamStatus) && !(procOrder.NotificationPending && mapProcurementUpstreamStatus(upstreamStatus) == "delivered") {
		logger.Warnw("procurement_callback_transition_rejected",
			"procurement_order_id", procOrder.ID,
			"current_status", procOrder.Status,
			"upstream_status", upstreamStatus,
		)
		return nil
	}

	switch upstreamStatus {
	case "delivered", "completed", "fulfilled":
		if fulfillment != nil && strings.TrimSpace(fulfillment.Status) != "" && !strings.EqualFold(strings.TrimSpace(fulfillment.Status), "delivered") {
			return fmt.Errorf("upstream fulfillment not delivered")
		}
		if fulfillment == nil || strings.TrimSpace(fulfillment.Payload) == "" {
			return fmt.Errorf("upstream delivery payload missing")
		}
		atomic, ok := s.orderLifecycle.(procurementcontract.AtomicDelivery)
		if !ok {
			return fmt.Errorf("atomic procurement delivery unavailable")
		}
		fulfilled := false
		if !procOrder.NotificationPending {
			fulfilled, err = atomic.Deliver(procOrder, fulfillment, now)
			if err != nil {
				return err
			}
		}
		if !fulfilled && !procOrder.NotificationPending {
			return nil
		}
		notificationsOK := true

		// 如果有父订单，同步父订单状态
		localOrder, _ := s.orderRepo.GetByID(procOrder.LocalOrderID)
		if localOrder != nil && localOrder.ParentID != nil && s.orderLifecycle != nil {
			if status, syncErr := s.orderLifecycle.SyncParentStatus(*localOrder.ParentID, now); syncErr != nil {
				notificationsOK = false
				logger.Warnw("procurement_sync_parent_status_failed",
					"procurement_order_id", procOrder.ID,
					"parent_order_id", *localOrder.ParentID,
					"error", syncErr,
				)
			} else {
				if status == "" {
					status = constants.OrderStatusDelivered
				}
				if status != constants.OrderStatusCanceled {
					if _, err := s.orderLifecycle.EnqueueStatusEmail(*localOrder.ParentID, status); err != nil {
						notificationsOK = false
					}
				}
			}
		} else if localOrder != nil && s.orderLifecycle != nil {
			if _, err := s.orderLifecycle.EnqueueStatusEmail(localOrder.ID, constants.OrderStatusDelivered); err != nil {
				notificationsOK = false
			}
		}

		// 触发下游回调（多级连跳：本站作为中间节点，通知下游交付完成）
		if s.downstreamCallback != nil {
			s.downstreamCallback.EnqueueCallback(procOrder.LocalOrderID)
			// 如果有父订单，也通知父订单的下游
			if localOrder != nil && localOrder.ParentID != nil {
				s.downstreamCallback.EnqueueCallback(*localOrder.ParentID)
			}
		}

		// 通知 Bot 订单已交付
		if s.botNotifier != nil && localOrder != nil {
			notifyOrderID := localOrder.ID
			if localOrder.ParentID != nil {
				notifyOrderID = *localOrder.ParentID
			}
			go s.botNotifier.NotifyBotOrderFulfilled(localOrder.UserID, notifyOrderID)
		}

		if localOrder == nil {
			notificationsOK = false
		}
		if notificationsOK {
			current, err := s.procRepo.GetByID(procOrder.ID)
			if err != nil {
				return err
			}
			if current != nil {
				_, err = s.procRepo.UpdateStatus(current.ID, current.Status, current.Status, map[string]interface{}{"notification_pending": false})
				if err != nil {
					return err
				}
			}
		}
		logger.Infow("procurement_order_fulfilled",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)

	case "canceled":
		updates := map[string]interface{}{
			"cancellation_pending": false,
			"updated_at":           now,
		}
		canceled, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusCanceled, updates)
		if err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}
		if !canceled {
			return nil
		}

		// 回退本地订单状态并通知管理员
		s.rollbackLocalOrderOnProcurementFailure(procOrder, "upstream canceled order")

		logger.Infow("procurement_order_canceled_by_upstream",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)
	case "refunded", "partially_refunded":
		updates := map[string]interface{}{
			"updated_at": now,
		}
		targetStatus := constants.ProcurementStatusPartiallyRefunded
		if upstreamStatus == "refunded" {
			targetStatus = constants.ProcurementStatusRefunded
		}
		if _, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, targetStatus, updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}
		if upstreamStatus == "partially_refunded" {
			if fulfillment != nil && strings.TrimSpace(fulfillment.Payload) != "" && (strings.TrimSpace(fulfillment.Status) == "" || strings.EqualFold(strings.TrimSpace(fulfillment.Status), "delivered")) {
				return s.HandleUpstreamCallback(procOrder.ID, "delivered", fulfillment)
			}
			if s.queue != nil {
				return s.queue.EnqueuePoll(procOrder.ID, 30*time.Second)
			}
		}
		logger.Infow("procurement_order_refunded",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
			"upstream_status", upstreamStatus,
			"local_status", targetStatus,
		)

	default:
		logger.Warnw("procurement_unknown_upstream_status",
			"procurement_order_id", procOrder.ID,
			"upstream_status", upstreamStatus,
		)
	}

	return nil
}

// createUpstreamFulfillment 在本地订单上创建上游交付记录
func (s *Service) createUpstreamFulfillment(orderID uint, uf *procurementcontract.Fulfillment, now time.Time) error {
	return s.orderLifecycle.CreateUpstreamFulfillment(orderID, uf, now)
}

// isUpstreamTransitionAllowed 上游回调允许的采购单状态转换：
// 已交付、已全额退款、已取消的采购单不再接受 delivered / canceled；
// 退款类回调不受限（部分退款可能先于交付到达）。
func isUpstreamTransitionAllowed(current, upstreamStatus string) bool {
	if current == constants.ProcurementStatusRefunded && upstreamStatus != "refunded" {
		return false
	}
	switch upstreamStatus {
	case "delivered", "completed", "fulfilled", "canceled":
		switch current {
		case constants.ProcurementStatusFulfilled, constants.ProcurementStatusCompleted,
			constants.ProcurementStatusRefunded, constants.ProcurementStatusCanceled:
			return false
		}
	}
	return true
}
