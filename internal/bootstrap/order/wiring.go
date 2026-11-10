package orderwiring

import (
	"github.com/XManCodeing/XMall/internal/app/container"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	ordertransport "github.com/XManCodeing/XMall/internal/modules/order/transport/http"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/platform/database/gormdb"
)

// Handlers contains every order HTTP entrypoint required by the router.
type Handlers struct {
	Admin       *ordertransport.AdminHandler
	AdminRefund *ordertransport.AdminRefundHandler
	User        *ordertransport.UserHandler
	Guest       *ordertransport.GuestHandler
	Preview     *ordertransport.PreviewHandler
	Create      *ordertransport.CreateHandler
}

// New assembles order transports and their composition-boundary adapters.
func New(c *container.Container) Handlers {
	return Handlers{
		Admin: ordertransport.NewAdminHandler(
			orderAdminQueryAdapter{orders: c.OrderService},
			orderAdminUserAdapter{users: c.UserStore},
			orderAdminCouponAdapter{coupons: c.CouponRepo},
			orderAdminPromotionAdapter{promotions: c.PromotionRepo},
			orderAdminPaymentAdapter{payments: c.PaymentStore},
			orderAdminPaymentChannelAdapter{channels: c.PaymentChannelStore},
		).WithMembership(membershipQuery(c), membershipstore.New(gormdb.DB)).WithMembershipLedger(membershipstore.New(gormdb.DB)),
		AdminRefund: NewAdminRefundHandler(c),
		User: ordertransport.NewUserHandler(
			orderUserQueryAdapter{orders: c.OrderService},
			orderUserPaymentChannelAdapter{payments: c.PaymentService},
			orderUserRefundRecordAdapter{records: c.OrderStore},
			orderUserLookupAdapter{users: c.UserStore},
		).WithMembershipSnapshots(membershipstore.New(gormdb.DB)),
		Guest: ordertransport.NewGuestHandler(
			orderGuestQueryAdapter{orders: c.OrderService},
			orderUserPaymentChannelAdapter{payments: c.PaymentService},
			orderUserRefundRecordAdapter{records: c.OrderStore},
		),
		Preview: ordertransport.NewPreviewHandler(
			orderPreviewAdapter{orders: c.OrderService},
		),
		Create: ordertransport.NewCreateHandler(
			orderCreateAdapter{orders: c.OrderService},
			orderUserPaymentChannelAdapter{payments: c.PaymentService},
			orderGuestCreateCaptchaAdapter{captcha: c.CaptchaService},
			orderPaymentCreatorAdapter{payments: c.PaymentService},
		),
	}
}

// membershipQuery 构造只读快照范围；缺少生产仓储时保持拒绝，测试容器可不装配。
func membershipQuery(c *container.Container) ordertransport.OrderQuery {
	if store, ok := c.OrderStore.(*orderstore.Store); ok {
		return orderAdminQueryAdapter{readonly: store.MembershipOnly()}
	}
	return nil
}

// NewAdminRefundHandler exposes the focused refund composition for integration
// tests and command surfaces that do not need the complete order handler set.
func NewAdminRefundHandler(c *container.Container) *ordertransport.AdminRefundHandler {
	refunds := orderAdminRefundAdapter{refunds: c.OrderRefundService}
	return ordertransport.NewAdminRefundHandler(
		refunds,
		refunds,
		orderAdminWalletRefundAdapter{refunds: c.OrderRefundService},
		orderAdminOrderLookupAdapter{orders: c.OrderStore},
		orderAdminStatusEmailAdapter{queue: c.QueueClient},
	)
}
