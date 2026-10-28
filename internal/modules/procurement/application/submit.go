package application

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/homles698698-lab/XMall/internal/constants"
	"github.com/homles698698-lab/XMall/internal/logger"
	"github.com/homles698698-lab/XMall/internal/modules/procurement/application/execution"
	procurementcontract "github.com/homles698698-lab/XMall/internal/modules/procurement/contract"
	procurementdomain "github.com/homles698698-lab/XMall/internal/modules/procurement/domain"
)

// SubmitToUpstream Worker 调用：向上游站点提交采购单
func (s *Service) SubmitToUpstream(procurementOrderID uint) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return procurementcontract.ErrNotFound
	}

	if procOrder.Status == "submitting" || procOrder.Status == "unknown" {
		return s.PollUpstreamStatus(procOrder.ID)
	}
	// Only pending has never crossed the HTTP boundary. Historical failures are ambiguous.
	if procOrder.Status == "failed" {
		_, err := s.procRepo.UpdateStatus(procOrder.ID, "failed", "unknown", map[string]interface{}{"error_message": "legacy failure requires verification"})
		return err
	}
	if procOrder.Status != "pending" {
		return procurementcontract.ErrStatusInvalid
	}

	// 获取连接和适配器
	connection, err := execution.Open(s.connections, procOrder)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load connection failed: %v", err))
		return fmt.Errorf("load connection: %w", err)
	}
	if connection == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("connection %d not found", procOrder.ConnectionID))
		return nil // 永久性错误，不重试
	}

	// 加载本地订单获取 SKU 信息
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil {
		s.markProcurementError(procOrder, fmt.Sprintf("load local order failed: %v", err))
		return fmt.Errorf("load local order: %w", err)
	}
	if localOrder == nil {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d not found", procOrder.LocalOrderID))
		return nil // 永久性错误，不重试
	}
	// 本地订单已退款/取消等不再需要采购（如巡检补投的历史积压单），不能再去上游扣款
	if localOrder.Status != constants.OrderStatusPaid && localOrder.Status != constants.OrderStatusFulfilling && localOrder.Status != constants.OrderStatusPartiallyRefunded {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d status %s no longer needs procurement", localOrder.ID, localOrder.Status))
		return nil // 永久性错误，不重试
	}
	if len(localOrder.Items) == 0 {
		s.rejectProcurement(procOrder, fmt.Sprintf("local order %d has no items", localOrder.ID))
		return nil // 永久性错误，不重试
	}
	item := localOrder.Items[0]

	if procOrder.ConnectionSnapshot == "" {
		if snapshots, ok := s.connections.(procurementcontract.SnapshotConnectionProvider); ok {
			procOrder.ConnectionSnapshot, err = snapshots.Capture(procOrder.ConnectionID)
			if err != nil {
				return err
			}
			if procOrder.ConnectionSnapshot != "" {
				connection, err = snapshots.OpenSnapshot(procOrder.ConnectionID, procOrder.ConnectionSnapshot)
				if err != nil {
					return err
				}
				if connection == nil {
					return procurementcontract.ErrConnectionNotFound
				}
			}
		}
	}
	// Frozen requests never consult mutable mappings again.
	req := procurementcontract.CreateOrderRequest{}
	if procOrder.RequestSnapshot != "" {
		if err := json.Unmarshal([]byte(procOrder.RequestSnapshot), &req); err != nil {
			return err
		}
	} else {
		upstreamSKUID, found, err := s.skuMapRepo.FindUpstreamSKUID(item.SKUID)
		if err != nil {
			return err
		}
		if !found {
			s.rejectProcurement(procOrder, "missing sku mapping")
			return nil
		}
		req = procurementcontract.CreateOrderRequest{SKUID: upstreamSKUID, Quantity: item.Quantity, DownstreamOrderNo: localOrder.OrderNo, TraceID: procOrder.TraceID, CallbackURL: connection.CallbackURL(), ManualFormData: item.ManualFormSubmissionJSON}
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return err
	}
	claimer, ok := s.procRepo.(procurementcontract.SubmissionClaimer)
	if !ok {
		return fmt.Errorf("atomic submission claim unavailable")
	}
	claimed, err := claimer.ClaimSubmission(procOrder, map[string]interface{}{"request_snapshot": string(encoded), "connection_snapshot": procOrder.ConnectionSnapshot, "updated_at": time.Now()})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	procOrder.Status = "submitting"
	// Recheck cancellation after durable claim, before any external request.
	localOrder, err = s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil {
		return err
	}
	if localOrder == nil || (localOrder.Status != constants.OrderStatusPaid && localOrder.Status != constants.OrderStatusFulfilling && localOrder.Status != constants.OrderStatusPartiallyRefunded) {
		s.rejectProcurement(procOrder, "local order no longer needs procurement")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := connection.CreateOrder(ctx, req)
	if err != nil || resp == nil {
		return s.handleSubmitFailure(procOrder, connection, "upstream outcome unknown; query or manual verification required", false)
	}

	if !resp.OK {
		retryable := isRetryableErrorCode(resp.ErrorCode)
		errMsg := resp.ErrorMessage
		if errMsg == "" {
			errMsg = resp.ErrorCode
		}
		return s.handleSubmitFailure(procOrder, connection, errMsg, retryable)
	}

	if resp.OrderID == 0 && strings.TrimSpace(resp.ExternalID) == "" {
		return s.handleSubmitFailure(procOrder, connection, "upstream identity missing; verification required", false)
	}
	// 成功：更新状态，重置 retry_count 用于轮询阶段
	now := time.Now()
	upstreamFields := map[string]interface{}{
		"upstream_order_id":          resp.OrderID,
		"upstream_order_external_id": resp.ExternalID,
		"upstream_order_no":          resp.OrderNo,
		"upstream_amount":            resp.Amount,
		"upstream_currency":          resp.Currency,
		"updated_at":                 now,
	}
	updates := maps.Clone(upstreamFields)
	updates["error_message"] = ""
	updates["retry_count"] = 0
	accepted, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusAccepted, updates)
	if err != nil {
		return fmt.Errorf("update procurement status: %w", err)
	}
	if !accepted {
		return s.recordUpstreamOrderAfterStatusChanged(procOrder.ID, resp.OrderNo, upstreamFields)
	}

	logger.Infow("procurement_order_accepted",
		"procurement_order_id", procOrder.ID,
		"upstream_order_id", resp.OrderID,
		"upstream_order_no", resp.OrderNo,
	)

	// Guard local status at write time; never revert fulfillment or refunds.
	if guarded, ok := s.procRepo.(procurementcontract.GuardedLocalStatus); ok {
		if err := guarded.UpdateLocalStatus(localOrder.ID, constants.OrderStatusFulfilling, now); err != nil {
			return err
		}
	}

	// 入队轮询任务（30s 延迟，作为回调的 fallback）
	if s.queue != nil {
		_ = s.queue.EnqueuePoll(procOrder.ID, 30*time.Second)
	}

	if resp.Fulfillment != nil && strings.EqualFold(strings.TrimSpace(resp.Fulfillment.Status), "delivered") {
		return s.HandleUpstreamCallback(procOrder.ID, "delivered", resp.Fulfillment)
	}
	return nil
}

// markProcurementError 记录错误信息但不改变状态（用于瞬态错误，asynq 可重试）
func (s *Service) markProcurementError(procOrder *procurementdomain.Order, errMsg string) {
	now := time.Now()
	_, _ = s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, procOrder.Status, map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	logger.Warnw("procurement_prepare_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
}

// rejectProcurement 将采购单标记为 rejected（用于永久性配置错误，不值得重试）
// 同时回退本地订单状态并通知管理员
func (s *Service) rejectProcurement(procOrder *procurementdomain.Order, errMsg string) {
	now := time.Now()
	rejected, _ := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, constants.ProcurementStatusRejected, map[string]interface{}{
		"error_message": errMsg,
		"updated_at":    now,
	})
	if !rejected {
		return
	}
	logger.Warnw("procurement_rejected_config_error",
		"procurement_order_id", procOrder.ID,
		"error", errMsg,
	)
	s.rollbackLocalOrderOnProcurementFailure(procOrder, errMsg)
}

// recordUpstreamOrderAfterStatusChanged 上游已成单，但采购单在请求途中被回调推进（如秒发卡密）或被管理员取消：
// 不回退状态，只补记上游订单号，否则对账与上游退款同步会跳过这单；被取消的需告警人工处理。
func (s *Service) recordUpstreamOrderAfterStatusChanged(id uint, upstreamOrderNo string, fields map[string]interface{}) error {
	current, err := s.procRepo.GetByID(id)
	if err != nil {
		return fmt.Errorf("reload procurement order: %w", err)
	}
	if current == nil {
		return procurementcontract.ErrNotFound
	}
	if _, err := s.procRepo.UpdateStatus(id, current.Status, current.Status, fields); err != nil {
		return fmt.Errorf("record upstream order: %w", err)
	}
	if current.Status == constants.ProcurementStatusCanceled {
		logger.Errorw("procurement_canceled_but_upstream_created",
			"procurement_order_id", id,
			"upstream_order_no", upstreamOrderNo,
		)
		s.notifyProcurementFailure(current, fmt.Sprintf("procurement canceled locally but upstream order %s was created and paid", upstreamOrderNo))
		return nil
	}
	logger.Infow("procurement_accept_skipped_status_changed", "procurement_order_id", id, "status", current.Status)
	return nil
}

// rollbackLocalOrderOnProcurementFailure 采购单终态失败时回退本地订单状态并通知管理员
func (s *Service) rollbackLocalOrderOnProcurementFailure(procOrder *procurementdomain.Order, errMsg string) {
	localOrder, err := s.orderRepo.GetByID(procOrder.LocalOrderID)
	if err != nil || localOrder == nil {
		return
	}
	if localOrder.Status == constants.OrderStatusFulfilling {
		now := time.Now()
		if guarded, ok := s.procRepo.(procurementcontract.GuardedLocalStatus); ok {
			_ = guarded.UpdateLocalStatus(localOrder.ID, constants.OrderStatusPaid, now)
		}
		// 如果是子订单，同步父订单状态
		if localOrder.ParentID != nil && s.orderLifecycle != nil {
			_, _ = s.orderLifecycle.SyncParentStatus(*localOrder.ParentID, now)
		}
		logger.Infow("procurement_failure_order_rolled_back",
			"procurement_order_id", procOrder.ID,
			"local_order_id", localOrder.ID,
			"from_status", constants.OrderStatusFulfilling,
			"to_status", constants.OrderStatusPaid,
		)
	}
	s.notifyProcurementFailure(procOrder, errMsg)
}

// notifyProcurementFailure 发送采购失败异常告警
func (s *Service) notifyProcurementFailure(procOrder *procurementdomain.Order, errMsg string) {
	if s.notifications == nil {
		return
	}
	_ = s.notifications.NotifyFailure(procOrder, errMsg)
}

// handleSubmitFailure 处理提交失败
func (s *Service) handleSubmitFailure(procOrder *procurementdomain.Order, connection procurementcontract.UpstreamConnection, errMsg string, retryable bool) error {
	changed, err := s.procRepo.UpdateStatus(procOrder.ID, procOrder.Status, "unknown", map[string]interface{}{"error_message": errMsg, "updated_at": time.Now()})
	if err != nil || !changed {
		return err
	}
	if s.queue != nil {
		return s.queue.EnqueuePoll(procOrder.ID, 30*time.Second)
	}
	return nil
}

// isRetryableErrorCode 判断上游错误码是否可重试
func isRetryableErrorCode(code string) bool {
	nonRetryable := map[string]bool{
		"insufficient_balance": true,
		"payment_failed":       true,
		"product_unavailable":  true,
		"sku_unavailable":      true,
		"invalid_request":      true,
		"unauthorized":         true,
		"forbidden":            true,
		"duplicate_order":      true,
		"product_out_of_stock": true,
	}
	return !nonRetryable[strings.ToLower(strings.TrimSpace(code))]
}

// parseRetryIntervals 解析重试间隔配置（JSON 数组格式如 "[30,60,300]"）
func parseRetryIntervals(raw string) []time.Duration {
	raw = strings.TrimSpace(raw)
	// 移除方括号
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")

	if raw == "" {
		return []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second}
	}

	parts := strings.Split(raw, ",")
	intervals := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		seconds, err := strconv.Atoi(part)
		if err != nil || seconds <= 0 {
			continue
		}
		intervals = append(intervals, time.Duration(seconds)*time.Second)
	}

	if len(intervals) == 0 {
		return []time.Duration{30 * time.Second, 60 * time.Second, 300 * time.Second}
	}
	return intervals
}
