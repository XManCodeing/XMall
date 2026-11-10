package paymenthttp

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/constants"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"

	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"

	ginutil "github.com/XManCodeing/XMall/internal/platform/http/ginutil"

	"github.com/XManCodeing/XMall/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

// ManualConfirmationRequest 是 HTTP 自有 DTO，字段与财务 contract 一致；操作者不接受 JSON 输入。
type ManualConfirmationRequest paymentcontract.ManualConfirmationInput

// ManualConfirmationHandler 必须挂在 admin JWT 与独立财务 Casbin 权限之后。
type ManualConfirmationHandler struct {
	service paymentcontract.ManualConfirmationService
}

// NewManualConfirmationHandler 注入原支付服务；空依赖拒绝装配。
func NewManualConfirmationHandler(service paymentcontract.ManualConfirmationService) *ManualConfirmationHandler {
	if service == nil {
		panic("payment manual confirmation handler: service is nil")
	}
	return &ManualConfirmationHandler{service: service}
}

// GetManualConfirmation 返回 URL 指定支付的安全快照，管理员身份仅从认证上下文取得。
func (h *ManualConfirmationHandler) GetManualConfirmation(c *gin.Context) {
	adminID, ok := ginutil.GetAdminID(c)
	if !ok || adminID == 0 {
		return
	}
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil || id == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}
	result, err := h.service.GetManualConfirmation(id)
	if err != nil {
		respondManualConfirmationError(c, err)
		return
	}
	response.Success(c, result)
}

// ConfirmManually 严格解析有限大小的 JSON，拒绝未知操作者字段及尾随内容；成功仅表示收款提交。
func (h *ManualConfirmationHandler) ConfirmManually(c *gin.Context) {
	adminID, ok := ginutil.GetAdminID(c)
	if !ok || adminID == 0 {
		return
	}
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil || id == 0 {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}
	var input ManualConfirmationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", nil)
		return
	}
	input.AdminID = adminID
	result, err := h.service.ConfirmManually(id, paymentcontract.ManualConfirmationInput(input))
	if err != nil {
		respondManualConfirmationError(c, err)
		return
	}
	response.Success(c, result)
}

// respondManualConfirmationError 映射受控错误，不泄露数据库细节或收款证据。
func respondManualConfirmationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrPaymentNotFound):
		ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
	case errors.Is(err, paymentcontract.ErrManualConfirmationInvalid):
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
	case errors.Is(err, paymentcontract.ErrManualConfirmationConflict):
		ginutil.RespondError(c, http.StatusConflict, "error.order_status_invalid", nil)
	case errors.Is(err, ErrMembershipSalesClosed):
		ginutil.RespondError(c, http.StatusConflict, "error.membership_sales_closed", nil)
	default:
		ginutil.RespondError(c, response.CodeInternal, "error.payment_update_failed", nil)
	}
}

const adminPaymentExportBatchSize = 500

// AdminPaymentListFilter 后台支付列表过滤条件。
type AdminPaymentListFilter struct {
	Page         int
	PageSize     int
	UserID       uint
	OrderID      uint
	ChannelID    uint
	ProviderType string
	ChannelType  string
	Status       string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	SkipCount    bool
	Lightweight  bool
}

// AdminPaymentQuery 后台支付查询端口。
type AdminPaymentQuery interface {
	ListPayments(filter AdminPaymentListFilter) ([]paymentdomain.Payment, int64, error)
	GetPayment(id uint) (*paymentdomain.Payment, error)
}

// AdminChannelLookup 后台支付渠道名称查询端口。
type AdminChannelLookup interface {
	ListByIDs(ids []uint) ([]paymentdomain.PaymentChannel, error)
}

// AdminOrderLookup 后台订单号查询端口。
type AdminOrderLookup interface {
	GetByIDs(ids []uint) ([]orderdomain.Order, error)
}

// AdminRechargeLookup 后台充值单元数据查询端口。
type AdminRechargeLookup interface {
	GetRechargeOrdersByPaymentIDs(paymentIDs []uint) ([]walletdomain.RechargeOrder, error)
}

// AdminPaymentItem 支付记录返回
type AdminPaymentItem struct {
	paymentdomain.Payment
	ChannelName        string `json:"channel_name"`
	DisplayChannelType string `json:"display_channel_type,omitempty"`
	OrderNo            string `json:"order_no,omitempty"`
	RechargeNo         string `json:"recharge_no,omitempty"`
	RechargeStatus     string `json:"recharge_status,omitempty"`
	RechargeUserID     uint   `json:"recharge_user_id,omitempty"`
}

type paymentRechargeMeta struct {
	RechargeNo string
	Status     string
	UserID     uint
}

// AdminHandler 处理后台支付只读 HTTP。
type AdminHandler struct {
	payments AdminPaymentQuery
	channels AdminChannelLookup
	orders   AdminOrderLookup
	recharge AdminRechargeLookup
}

func NewAdminHandler(payments AdminPaymentQuery, channels AdminChannelLookup, orders AdminOrderLookup, recharge AdminRechargeLookup) *AdminHandler {
	if payments == nil {
		panic("payment admin handler: payments is nil")
	}
	return &AdminHandler{payments: payments, channels: channels, orders: orders, recharge: recharge}
}

// GetAdminPayments 获取支付记录列表
func (h *AdminHandler) GetAdminPayments(c *gin.Context) {
	page, pageSize := ginutil.ParsePagination(c)

	filter, err := buildAdminPaymentFilter(c, page, pageSize)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}

	payments, total, err := h.payments.ListPayments(filter)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	pagination := response.BuildPagination(page, pageSize, total)
	channelNameMap, err := h.resolvePaymentChannelNames(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	orderNoMap, err := h.resolvePaymentOrderNos(payments)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	items := make([]AdminPaymentItem, 0, len(payments))
	for _, payment := range payments {
		rechargeMeta := rechargeMetaMap[payment.ID]
		safePayment := redactAdminPayment(payment)
		items = append(items, AdminPaymentItem{
			Payment:            safePayment,
			ChannelName:        channelNameMap[payment.ChannelID],
			DisplayChannelType: paymentDisplayChannelType(payment),
			OrderNo:            orderNoMap[payment.OrderID],
			RechargeNo:         rechargeMeta.RechargeNo,
			RechargeStatus:     rechargeMeta.Status,
			RechargeUserID:     rechargeMeta.UserID,
		})
	}

	response.SuccessWithPage(c, items, pagination)
}

// ExportAdminPayments 导出支付记录 CSV
func (h *AdminHandler) ExportAdminPayments(c *gin.Context) {
	filter, err := buildAdminPaymentFilter(c, 1, adminPaymentExportBatchSize)
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.bad_request", err)
		return
	}
	filter.SkipCount = true
	filter.Lightweight = true

	payments, _, err := h.payments.ListPayments(filter)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}

	filename := fmt.Sprintf("payments_%s.csv", constants.DisplayTime(time.Now(), "20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	writer := csv.NewWriter(c.Writer)
	if err := writer.Write([]string{
		"id",
		"order_id",
		"recharge_no",
		"recharge_status",
		"recharge_user_id",
		"channel_id",
		"provider_type",
		"channel_type",
		"display_channel_type",
		"status",
		"amount",
		"currency",
		"created_at",
		"paid_at",
		"expired_at",
		"provider_ref",
	}); err != nil {
		ginutil.RequestLog(c).Errorw("admin_payment_export_header_write_failed", "error", err)
		return
	}

	page := 1
	for {
		if len(payments) > 0 {
			if err := h.writeAdminPaymentCSVRows(writer, payments); err != nil {
				ginutil.RequestLog(c).Errorw("admin_payment_export_rows_write_failed", "page", page, "error", err)
				return
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				ginutil.RequestLog(c).Errorw("admin_payment_export_flush_failed", "page", page, "error", err)
				return
			}
		}
		if len(payments) < adminPaymentExportBatchSize {
			break
		}
		page++
		filter.Page = page
		payments, _, err = h.payments.ListPayments(filter)
		if err != nil {
			ginutil.RequestLog(c).Errorw("admin_payment_export_batch_fetch_failed", "page", page, "error", err)
			return
		}
	}
}

// GetAdminPayment 获取支付记录详情
func (h *AdminHandler) GetAdminPayment(c *gin.Context) {
	id, err := ginutil.ParseParamUint(c, "id")
	if err != nil {
		ginutil.RespondError(c, response.CodeBadRequest, "error.payment_invalid", nil)
		return
	}

	payment, err := h.payments.GetPayment(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrPaymentNotFound):
			ginutil.RespondError(c, response.CodeNotFound, "error.payment_not_found", nil)
		default:
			ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		}
		return
	}

	channelNameMap, err := h.resolvePaymentChannelNames([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	orderNoMap, err := h.resolvePaymentOrderNos([]paymentdomain.Payment{*payment})
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.payment_fetch_failed", err)
		return
	}
	rechargeMeta := rechargeMetaMap[payment.ID]
	safePayment := redactAdminPayment(*payment)
	response.Success(c, AdminPaymentItem{
		Payment:            safePayment,
		ChannelName:        channelNameMap[payment.ChannelID],
		DisplayChannelType: paymentDisplayChannelType(*payment),
		OrderNo:            orderNoMap[payment.OrderID],
		RechargeNo:         rechargeMeta.RechargeNo,
		RechargeStatus:     rechargeMeta.Status,
		RechargeUserID:     rechargeMeta.UserID,
	})
}

func redactAdminPayment(payment paymentdomain.Payment) paymentdomain.Payment {
	payment.ProviderPayload = nil
	payment.PayURL = ""
	payment.QRCode = ""
	return payment
}

// paymentDisplayChannelType 提取后台支付记录的展示用渠道类型。
// CSV 导出的 lightweight 查询会把 provider_payload.display_channel_type 提取到 Payment.DisplayChannelType；
// 后台列表和详情会在响应前清空 ProviderPayload，因此必须在脱敏前从 payload 兜底读取。
func paymentDisplayChannelType(payment paymentdomain.Payment) string {
	if displayChannelType := strings.TrimSpace(payment.DisplayChannelType); displayChannelType != "" {
		return displayChannelType
	}
	value, ok := payment.ProviderPayload["display_channel_type"]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func formatTimeNullable(raw *time.Time) string {
	if raw == nil {
		return ""
	}
	return constants.DisplayTime(*raw, time.RFC3339)
}

func buildAdminPaymentFilter(c *gin.Context, page, pageSize int) (AdminPaymentListFilter, error) {
	orderID, err := ginutil.ParseQueryUint(c.Query("order_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}
	userID, err := ginutil.ParseQueryUint(c.Query("user_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}
	channelID, err := ginutil.ParseQueryUint(c.Query("channel_id"), true)
	if err != nil {
		return AdminPaymentListFilter{}, err
	}

	createdFrom, createdTo, err := ginutil.ParseQueryTimeRange(c, "created_from", "created_to")
	if err != nil {
		return AdminPaymentListFilter{}, err
	}

	return AdminPaymentListFilter{
		Page:         page,
		PageSize:     pageSize,
		UserID:       userID,
		OrderID:      orderID,
		ChannelID:    channelID,
		ProviderType: strings.TrimSpace(c.Query("provider_type")),
		ChannelType:  strings.TrimSpace(c.Query("channel_type")),
		Status:       strings.TrimSpace(c.Query("status")),
		CreatedFrom:  createdFrom,
		CreatedTo:    createdTo,
	}, nil
}

func (h *AdminHandler) writeAdminPaymentCSVRows(writer *csv.Writer, payments []paymentdomain.Payment) error {
	rechargeMetaMap, err := h.resolvePaymentRechargeMeta(payments)
	if err != nil {
		return err
	}
	for _, payment := range payments {
		rechargeMeta := rechargeMetaMap[payment.ID]
		if err := writer.Write([]string{
			strconv.FormatUint(uint64(payment.ID), 10),
			strconv.FormatUint(uint64(payment.OrderID), 10),
			rechargeMeta.RechargeNo,
			rechargeMeta.Status,
			strconv.FormatUint(uint64(rechargeMeta.UserID), 10),
			strconv.FormatUint(uint64(payment.ChannelID), 10),
			payment.ProviderType,
			payment.ChannelType,
			paymentDisplayChannelType(payment),
			payment.Status,
			payment.Amount.String(),
			payment.Currency,
			constants.DisplayTime(payment.CreatedAt, time.RFC3339),
			formatTimeNullable(payment.PaidAt),
			formatTimeNullable(payment.ExpiredAt),
			payment.ProviderRef,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *AdminHandler) resolvePaymentChannelNames(payments []paymentdomain.Payment) (map[uint]string, error) {
	channelIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.ChannelID == 0 {
			continue
		}
		if _, ok := seen[payment.ChannelID]; ok {
			continue
		}
		seen[payment.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, payment.ChannelID)
	}
	result := make(map[uint]string)
	if len(channelIDs) == 0 || h.channels == nil {
		return result, nil
	}
	channels, err := h.channels.ListByIDs(channelIDs)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		result[channel.ID] = channel.Name
	}
	return result, nil
}

func (h *AdminHandler) resolvePaymentOrderNos(payments []paymentdomain.Payment) (map[uint]string, error) {
	orderIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.OrderID == 0 {
			continue
		}
		if _, ok := seen[payment.OrderID]; ok {
			continue
		}
		seen[payment.OrderID] = struct{}{}
		orderIDs = append(orderIDs, payment.OrderID)
	}
	result := make(map[uint]string)
	if len(orderIDs) == 0 || h.orders == nil {
		return result, nil
	}
	orders, err := h.orders.GetByIDs(orderIDs)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		result[order.ID] = strings.TrimSpace(order.OrderNo)
	}
	return result, nil
}

func (h *AdminHandler) resolvePaymentRechargeMeta(payments []paymentdomain.Payment) (map[uint]paymentRechargeMeta, error) {
	paymentIDs := make([]uint, 0, len(payments))
	seen := make(map[uint]struct{})
	for _, payment := range payments {
		if payment.ID == 0 {
			continue
		}
		if _, ok := seen[payment.ID]; ok {
			continue
		}
		seen[payment.ID] = struct{}{}
		paymentIDs = append(paymentIDs, payment.ID)
	}
	result := make(map[uint]paymentRechargeMeta)
	if len(paymentIDs) == 0 || h.recharge == nil {
		return result, nil
	}
	orders, err := h.recharge.GetRechargeOrdersByPaymentIDs(paymentIDs)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		result[order.PaymentID] = paymentRechargeMeta{
			RechargeNo: strings.TrimSpace(order.RechargeNo),
			Status:     strings.TrimSpace(order.Status),
			UserID:     order.UserID,
		}
	}
	return result, nil
}
