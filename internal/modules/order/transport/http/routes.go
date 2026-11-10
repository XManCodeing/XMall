package orderhttp

import (
	"context"
	"errors"
	"strings"
	"time"

	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/ginutil"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// membershipListConstraint validates HTTP dimensions and resolves item IDs before count/paging.
// ctx is authorized; ok=false means an error response has already been written.
func (h *AdminHandler) membershipListConstraint(ctx *gin.Context) (ids []uint, filtered, ok bool) {
	filter := membership.AdminOrderStateFilter{Rights: strings.TrimSpace(ctx.Query("rights")), MemberRemoval: strings.TrimSpace(ctx.Query("member_removal")), InviteCleanup: strings.TrimSpace(ctx.Query("invite_cleanup"))}
	if filter.Empty() {
		return nil, false, true
	}
	if filter.Validate() != nil || h.detailSnapshots == nil {
		ginutil.RespondError(ctx, response.CodeBadRequest, "error.bad_request", nil)
		return nil, false, false
	}
	matcher, available := h.membershipLedger.(membership.AdminOrderStateMatcher)
	if !available {
		ginutil.RespondError(ctx, response.CodeInternal, "error.order_fetch_failed", nil)
		return nil, false, false
	}
	ids, err := matcher.MatchingOrderItems(ctx.Request.Context(), filter, time.Now().UTC())
	if err != nil {
		ginutil.RespondError(ctx, response.CodeInternal, "error.order_fetch_failed", err)
		return nil, false, false
	}
	return ids, true, true
}

// enrichMembershipList projects authorized root/child IDs; ordinary orders are untouched.
// Missing evidence is omitted, never assumed successful; foreign results fail closed.
func (h *AdminHandler) enrichMembershipList(ctx context.Context, items []AdminOrderListItem) error {
	if h.detailSnapshots == nil {
		return nil
	}
	reader, ok := h.membershipLedger.(membership.AdminOrderStateReader)
	if !ok {
		return nil
	}
	owners := map[uint]int{}
	ids := []uint{}
	for index, order := range items {
		for _, item := range order.Items {
			owners[item.ID] = index
			ids = append(ids, item.ID)
		}
		for _, child := range order.Children {
			for _, item := range child.Items {
				owners[item.ID] = index
				ids = append(ids, item.ID)
			}
		}
	}
	groups, err := reader.OrderStates(ctx, ids, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, group := range groups {
		index, found := owners[group.ItemID]
		if !found {
			return errors.New("membership state outside authorized orders")
		}
		items[index].MembershipGroups = append(items[index].MembershipGroups, group)
	}
	return nil
}

// RegisterAdminRoutes 注册后台订单路由（列表/详情/状态更新）。
func RegisterAdminRoutes(authorized gin.IRoutes, handler *AdminHandler) {
	if authorized == nil || handler == nil {
		panic("order admin routes: required dependency is nil")
	}
	authorized.GET("/orders", handler.AdminListOrders)
	authorized.GET("/orders/:id", handler.AdminGetOrder)
	authorized.PATCH("/orders/:id", handler.AdminUpdateOrderStatus)
}

// WithMembership 装配快照范围的只读查询；返回原 handler，不复制订单呈现或交易。
func (h *AdminHandler) WithMembership(query OrderQuery, snapshots membership.SnapshotReader) *AdminHandler {
	h.membershipOrders = query
	h.membershipSnapshots = snapshots
	return h
}

// WithMembershipLedger adds an admin-only ledger reader; existing route authorization remains unchanged.
// reader receives only item IDs from the already authorized membership order detail.
func (h *AdminHandler) WithMembershipLedger(reader membership.AdminLedgerReader) *AdminHandler {
	h.membershipLedger = reader
	return h
}

// RegisterMembershipRoutes 复用详情呈现，仅暴露会员订单列表和详情。
func RegisterMembershipRoutes(authorized gin.IRoutes, handler *AdminHandler) {
	scoped := *handler
	scoped.orders = handler.membershipOrders
	scoped.detailSnapshots = handler.membershipSnapshots
	guard := func(c *gin.Context) {
		if scoped.orders == nil || scoped.detailSnapshots == nil {
			ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
			c.Abort()
		}
	}
	authorized.GET("/telegram-bot/membership/orders", guard, scoped.AdminListOrders)
	authorized.GET("/telegram-bot/membership/orders/:id", guard, scoped.AdminGetOrder)
}

// RegisterAdminRefundRoutes 注册后台退款只读路由。
func RegisterAdminRefundRoutes(authorized gin.IRoutes, handler *AdminRefundHandler) {
	if authorized == nil || handler == nil {
		panic("order admin refund routes: required dependency is nil")
	}
	authorized.GET("/order-refunds", handler.GetAdminOrderRefunds)
	authorized.GET("/order-refunds/:id", handler.GetAdminOrderRefund)
}

// RegisterAdminRefundWriteRoutes 注册后台退款写路由。
func RegisterAdminRefundWriteRoutes(authorized gin.IRoutes, handler *AdminRefundHandler) {
	if authorized == nil || handler == nil {
		panic("order admin refund write routes: required dependency is nil")
	}
	authorized.POST("/orders/:id/refund-to-wallet", handler.AdminRefundOrderToWallet)
	authorized.POST("/orders/:id/manual-refund", handler.AdminManualRefundOrder)
	authorized.PATCH("/order-refunds/:id/payment-fee", handler.UpdateAdminOrderRefundPaymentFee)
}

// RegisterUserReadRoutes 注册前台用户订单只读路由。
func RegisterUserReadRoutes(user gin.IRoutes, handler *UserHandler) {
	if user == nil || handler == nil {
		panic("order user read routes: required dependency is nil")
	}
	user.GET("/orders", handler.ListOrders)
	user.GET("/orders/stats", handler.OrderStats)
	user.GET("/orders/:order_no", handler.GetOrderByOrderNo)
	user.GET("/orders/:order_no/fulfillment/download", handler.DownloadFulfillment)
}

// RegisterUserPaymentChannelsRoute 注册前台用户订单可用支付渠道查询路由。
func RegisterUserPaymentChannelsRoute(user gin.IRoutes, handler *UserHandler) {
	if user == nil || handler == nil {
		panic("order user payment channels route: required dependency is nil")
	}
	user.POST("/order/payment-channels", handler.GetOrderPaymentChannels)
}

// RegisterUserCancelRoute 注册前台用户取消订单路由。
func RegisterUserCancelRoute(user gin.IRoutes, handler *UserHandler) {
	if user == nil || handler == nil {
		panic("order user cancel route: required dependency is nil")
	}
	user.POST("/orders/:order_no/cancel", handler.CancelOrder)
}

// RegisterGuestReadRoutes 注册前台游客订单只读路由。
func RegisterGuestReadRoutes(guest gin.IRoutes, handler *GuestHandler) {
	if guest == nil || handler == nil {
		panic("order guest read routes: required dependency is nil")
	}
	guest.GET("/orders", handler.ListGuestOrders)
	guest.GET("/orders/:order_no", handler.GetGuestOrderByOrderNo)
	guest.GET("/orders/:order_no/fulfillment/download", handler.DownloadGuestFulfillment)
}

// RegisterUserPreviewRoute 注册前台用户订单预览路由。
func RegisterUserPreviewRoute(user gin.IRoutes, handler *PreviewHandler) {
	if user == nil || handler == nil {
		panic("order user preview route: required dependency is nil")
	}
	user.POST("/orders/preview", handler.PreviewOrder)
}

// RegisterGuestPreviewRoute 注册前台游客订单预览路由。
func RegisterGuestPreviewRoute(guest gin.IRoutes, handler *PreviewHandler) {
	if guest == nil || handler == nil {
		panic("order guest preview route: required dependency is nil")
	}
	guest.POST("/orders/preview", handler.PreviewGuestOrder)
}

// RegisterUserCreateRoute 注册前台用户创建订单路由。
func RegisterUserCreateRoute(user gin.IRoutes, handler *CreateHandler) {
	if user == nil || handler == nil {
		panic("order user create route: required dependency is nil")
	}
	user.POST("/orders", handler.CreateOrder)
}

// RegisterUserCreateAndPayRoute 注册前台用户创建订单并发起支付路由。
func RegisterUserCreateAndPayRoute(user gin.IRoutes, handler *CreateHandler) {
	if user == nil || handler == nil {
		panic("order user create-and-pay route: required dependency is nil")
	}
	user.POST("/orders/create-and-pay", handler.CreateOrderAndPay)
}

// RegisterGuestCreateRoute 注册前台游客创建订单路由。
func RegisterGuestCreateRoute(guest gin.IRoutes, handler *CreateHandler) {
	if guest == nil || handler == nil {
		panic("order guest create route: required dependency is nil")
	}
	guest.POST("/orders", handler.CreateGuestOrder)
}

// RegisterGuestCreateAndPayRoute 注册前台游客创建订单并发起支付路由。
func RegisterGuestCreateAndPayRoute(guest gin.IRoutes, handler *CreateHandler) {
	if guest == nil || handler == nil {
		panic("order guest create-and-pay route: required dependency is nil")
	}
	guest.POST("/orders/create-and-pay", handler.CreateGuestOrderAndPay)
}

// membershipOrderItem 仅用于会员详情，普通订单领域模型与 JSON 不增加会员字段。
type membershipOrderItem struct {
	orderdomain.OrderItem
	Snapshot *membership.OrderItemSnapshot `json:"membership_snapshot,omitempty"`
	Ledger   *membership.AdminItemLedger   `json:"membership_ledger,omitempty"`
}

// membershipChildOrder 覆盖子订单的 items，其他字段沿用已脱敏的原订单。
type membershipChildOrder struct {
	orderdomain.Order
	Items []membershipOrderItem `json:"items"`
}

// membershipOrderDetail 覆盖顶层及子单 items，保留普通详情的权限和支付呈现。
type membershipOrderDetail struct {
	AdminOrderDetail
	Items    []membershipOrderItem  `json:"items"`
	Children []membershipChildOrder `json:"children,omitempty"`
}

// respondAdminOrderDetail 接收已授权且已裁剪交付内容的详情；仅会员路由读取快照。
// 任何仓储错误均阻止返回部分详情，普通路由不调用会员仓储。
func (h *AdminHandler) respondAdminOrderDetail(c *gin.Context, detail AdminOrderDetail) {
	if h.detailSnapshots == nil {
		response.Success(c, detail)
		return
	}
	cache := make(map[uint]*membership.OrderItemSnapshot)
	items, err := membershipItems(c.Request.Context(), h.detailSnapshots, detail.Items, cache)
	if err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
		return
	}
	result := membershipOrderDetail{AdminOrderDetail: detail, Items: items}
	for _, child := range detail.Children {
		items, err := membershipItems(c.Request.Context(), h.detailSnapshots, child.Items, cache)
		if err != nil {
			ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
			return
		}
		result.Children = append(result.Children, membershipChildOrder{Order: child, Items: items})
	}
	if err := h.enrichAdminLedger(c.Request.Context(), &result); err != nil {
		ginutil.RespondError(c, response.CodeInternal, "error.order_fetch_failed", nil)
		return
	}
	response.Success(c, result)
}

// enrichAdminLedger reads only snapshot-backed IDs from an authorized detail and rejects foreign results.
// ctx propagates cancellation; result is changed only after the complete ledger response is validated.
func (h *AdminHandler) enrichAdminLedger(ctx context.Context, result *membershipOrderDetail) error {
	if h.membershipLedger == nil {
		return errors.New("membership ledger reader unavailable")
	}
	allowed := make(map[uint]bool)
	collect := func(items []membershipOrderItem) {
		for _, item := range items {
			if item.Snapshot != nil {
				allowed[item.ID] = true
			}
		}
	}
	collect(result.Items)
	for _, child := range result.Children {
		collect(child.Items)
	}
	ids := make([]uint, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := h.membershipLedger.OrderLedger(ctx, ids)
	if err != nil {
		return errors.New("membership ledger query failed")
	}
	byID := make(map[uint]membership.AdminItemLedger)
	for _, row := range rows {
		if !allowed[row.ItemID] {
			return errors.New("membership ledger item outside authorized detail")
		}
		if _, duplicate := byID[row.ItemID]; duplicate {
			return errors.New("duplicate membership ledger item")
		}
		byID[row.ItemID] = row
	}
	apply := func(items []membershipOrderItem) {
		for index := range items {
			if row, found := byID[items[index].ID]; found {
				items[index].Ledger = &row
			}
		}
	}
	apply(result.Items)
	for index := range result.Children {
		apply(result.Children[index].Items)
	}
	return nil
}

// membershipItems 只按已授权详情中的订单项 ID 读取历史；cache 避免聚合父项与子项重复查询。
// 没有快照的普通混合订单项保持缺省字段，其他错误原样返回，不查现配置。
func membershipItems(ctx context.Context, reader membership.SnapshotReader, items []orderdomain.OrderItem, cache map[uint]*membership.OrderItemSnapshot) ([]membershipOrderItem, error) {
	result := make([]membershipOrderItem, 0, len(items))
	for _, item := range items {
		snapshot, found := cache[item.ID]
		if !found {
			stored, err := reader.Snapshot(ctx, item.ID)
			if err != nil && !errors.Is(err, membership.ErrNotFound) {
				return nil, err
			}
			if err == nil && stored != nil {
				value := membership.OrderItemSnapshot(*stored)
				snapshot = &value
			}
			cache[item.ID] = snapshot
		}
		result = append(result, membershipOrderItem{OrderItem: item, Snapshot: snapshot})
	}
	return result, nil
}
