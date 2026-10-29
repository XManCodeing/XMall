package httpserver

import (
	"github.com/XManCodeing/XMall/internal/app/container"
	"github.com/XManCodeing/XMall/internal/app/httpserver/middleware"
	affiliatebootstrap "github.com/XManCodeing/XMall/internal/bootstrap/affiliate"
	settingsbootstrap "github.com/XManCodeing/XMall/internal/bootstrap/settingshttp"
	telegrambootstrap "github.com/XManCodeing/XMall/internal/bootstrap/telegrambroadcast"
	"github.com/XManCodeing/XMall/internal/config"
	affiliatetransport "github.com/XManCodeing/XMall/internal/modules/affiliate/transport/http"
	apicredentialtransport "github.com/XManCodeing/XMall/internal/modules/apicredential/transport/http"
	auditlogtransport "github.com/XManCodeing/XMall/internal/modules/auditlog/transport/http"
	cardsecrettransport "github.com/XManCodeing/XMall/internal/modules/cardsecret/transport/http"
	categoryhttp "github.com/XManCodeing/XMall/internal/modules/catalog/category/transport/http"
	mappinghttp "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/transport/http"
	producthttp "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/http"
	contenttransport "github.com/XManCodeing/XMall/internal/modules/content/transport/http"
	coupontransport "github.com/XManCodeing/XMall/internal/modules/coupon/transport/http"
	dashboardtransport "github.com/XManCodeing/XMall/internal/modules/dashboard/transport/http"
	fulfillmenttransport "github.com/XManCodeing/XMall/internal/modules/fulfillment/transport/http"
	giftcardtransport "github.com/XManCodeing/XMall/internal/modules/giftcard/transport/http"
	adminauthtransport "github.com/XManCodeing/XMall/internal/modules/identity/adminauth/transport/http"
	adminauthztransport "github.com/XManCodeing/XMall/internal/modules/identity/adminauthorization/transport/http"
	invitecodetransport "github.com/XManCodeing/XMall/internal/modules/identity/invitecode/transport/http"
	adminusertransport "github.com/XManCodeing/XMall/internal/modules/identity/user/transport/http/admin"
	memberleveltransport "github.com/XManCodeing/XMall/internal/modules/memberlevel/transport/http"
	notificationtransport "github.com/XManCodeing/XMall/internal/modules/notification/transport/http"
	ordertransport "github.com/XManCodeing/XMall/internal/modules/order/transport/http"
	paymenttransport "github.com/XManCodeing/XMall/internal/modules/payment/transport/http"
	procurementtransport "github.com/XManCodeing/XMall/internal/modules/procurement/transport/http"
	promotiontransport "github.com/XManCodeing/XMall/internal/modules/promotion/transport/http"
	reconciliationtransport "github.com/XManCodeing/XMall/internal/modules/reconciliation/transport/http"
	resellertransport "github.com/XManCodeing/XMall/internal/modules/reseller/transport/http/admin"
	settingstransport "github.com/XManCodeing/XMall/internal/modules/settings/transport/http"
	siteconnectiontransport "github.com/XManCodeing/XMall/internal/modules/siteconnection/transport/http"
	broadcasthttp "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/transport/http"
	cutoverhttp "github.com/XManCodeing/XMall/internal/modules/telegram/cutover/transport/http"
	runtimehttp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/transport/http"
	uploadtransport "github.com/XManCodeing/XMall/internal/modules/upload/transport/http"
	wallettransport "github.com/XManCodeing/XMall/internal/modules/wallet/transport/http"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	systemtransport "github.com/XManCodeing/XMall/internal/platform/http/system"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func registerAdminRoutes(
	engine *gin.Engine,
	apiV1 *gin.RouterGroup,
	cfg *config.Config,
	c *container.Container,
	adminLoginHandler *adminauthtransport.AdminLoginHandler,
	admin2FAHandler *adminauthtransport.Admin2FAHandler,
	adminUser2FAHandler *adminauthtransport.AdminUser2FAHandler,
	adminUserHandler *adminusertransport.AdminHandler,
	adminAuthzHandler *adminauthztransport.AdminHandler,
	adminFulfillmentHandler *fulfillmenttransport.AdminHandler,
	adminOrderHandler *ordertransport.AdminHandler,
	adminOrderRefundHandler *ordertransport.AdminRefundHandler,
	adminContentHandler *contenttransport.AdminHandler,
	adminDashboardHandler *dashboardtransport.AdminHandler,
	adminMemberLevelHandler *memberleveltransport.AdminHandler,
	adminApiCredentialHandler *apicredentialtransport.AdminHandler,
	adminAuditLogHandler *auditlogtransport.AdminHandler,
	adminCardSecretHandler *cardsecrettransport.AdminHandler,
	adminCatalogCategoryHandler *categoryhttp.AdminCategoryHandler,
	adminCatalogProductHandler *producthttp.AdminProductHandler,
	adminCatalogProductMappingHandler *mappinghttp.AdminHandler,
	adminCouponHandler *coupontransport.AdminHandler,
	adminGiftCardHandler *giftcardtransport.AdminHandler,
	adminPromotionHandler *promotiontransport.AdminHandler,
	adminNotificationHandler *notificationtransport.AdminHandler,
	adminProcurementHandler *procurementtransport.AdminHandler,
	adminResellerManagementHandler *resellertransport.AdminManagementHandler,
	adminResellerProfileDetailHandler *resellertransport.AdminProfileDetailHandler,
	adminResellerSiteConfigHandler *resellertransport.AdminSiteConfigHandler,
	adminResellerProductSettingHandler *resellertransport.AdminProductSettingHandler,
	adminResellerOperationsHandler *resellertransport.AdminOperationsHandler,
	adminResellerFinanceHandler *resellertransport.AdminFinanceHandler,
	adminSettingsHandler *settingstransport.AdminHandler,
	adminWalletHandler *wallettransport.AdminHandler,
	adminPaymentHandler *paymenttransport.AdminHandler,
	adminPaymentChannelHandler *paymenttransport.AdminChannelHandler,
	redisClient *redis.Client,
	adminLoginRule middleware.RateLimitRule,
) {
	admin := apiV1.Group("/admin")

	// 登录接口（无需鉴权）
	adminauthtransport.RegisterAdminLoginAuthRoutes(admin, adminLoginHandler, middleware.RateLimitMiddleware(redisClient, adminLoginRule, middleware.KeyByIP))
	adminauthtransport.RegisterAdmin2FAAuthRoutes(admin, admin2FAHandler, middleware.RateLimitMiddleware(redisClient, adminLoginRule, middleware.KeyByIP))

	// 需要鉴权的接口
	authorized := admin.Use(middleware.JWTAuthMiddleware(cfg.JWT.SecretKey, c.AdminStore), middleware.AdminRBACMiddleware(c.AuthzService))
	// 仪表盘
	dashboardtransport.RegisterAdminRoutes(authorized, adminDashboardHandler)

	// 商品 / 分类管理
	producthttp.RegisterAdminRoutes(authorized, adminCatalogProductHandler)
	contenttransport.RegisterAdminRoutes(authorized, adminContentHandler)
	categoryhttp.RegisterAdminRoutes(authorized, adminCatalogCategoryHandler)

	// 设置管理
	settingstransport.RegisterAdminRoutes(authorized, adminSettingsHandler)
	settingstransport.RegisterAdminSMTPRoutes(authorized, settingsbootstrap.NewSMTPHandler(c, cfg))
	settingstransport.RegisterAdminCaptchaRoutes(authorized, settingsbootstrap.NewCaptchaHandler(c, cfg))
	settingstransport.RegisterAdminTelegramAuthRoutes(authorized, settingsbootstrap.NewTelegramAuthHandler(c, cfg))
	settingstransport.RegisterAdminGoogleAuthRoutes(authorized, settingsbootstrap.NewGoogleAuthHandler(c, cfg))
	notificationtransport.RegisterAdminRoutes(authorized, adminNotificationHandler)
	settingstransport.RegisterAdminOrderEmailTemplateRoutes(authorized, settingstransport.NewOrderEmailTemplateHandler(c.SettingService))
	settingstransport.RegisterAdminAffiliateRoutes(authorized, settingstransport.NewAffiliateHandler(c.SettingService))
	settingstransport.RegisterAdminTelegramBotRoutes(authorized, settingstransport.NewTelegramBotHandler(c.SettingService))
	cutoverhttp.RegisterRoutes(authorized, &cutoverhttp.Handler{Service: telegrambootstrap.NewManagedCutover(c.TelegramBotManager, c.ChannelClientService, c.OrderStore, c.TelegramBotLeaseClient)})
	if c.TelegramBotManager != nil {
		runtimehttp.RegisterRoutes(authorized, runtimehttp.New(c.TelegramBotManager))
	}
	adminauthtransport.RegisterAdminPasswordRoutes(authorized, adminLoginHandler)

	// 系统信息与版本检测
	systemtransport.RegisterAdminRoutes(authorized, systemtransport.NewAdminHandler(nil, cfg.Update.Repository))

	adminauthtransport.RegisterAdmin2FARoutes(authorized, admin2FAHandler)

	// 推广返利
	adminAffiliateHandler := affiliatebootstrap.NewAdminHandler(c)
	affiliatetransport.RegisterAdminRoutes(authorized, adminAffiliateHandler)
	affiliatetransport.RegisterAdminFinanceRoutes(authorized, adminAffiliateHandler)
	resellertransport.RegisterOperationsOverviewRoutes(authorized, adminResellerOperationsHandler)
	resellertransport.RegisterManagementRoutes(authorized, adminResellerManagementHandler)
	resellertransport.RegisterProfileDetailRoutes(authorized, adminResellerProfileDetailHandler)
	resellertransport.RegisterSiteConfigRoutes(authorized, adminResellerSiteConfigHandler)
	resellertransport.RegisterProductSettingRoutes(authorized, adminResellerProductSettingHandler)
	resellertransport.RegisterOperationsFinanceRoutes(authorized, adminResellerOperationsHandler)
	resellertransport.RegisterFinanceRoutes(authorized, adminResellerFinanceHandler)

	// 权限管理
	adminauthztransport.RegisterAdminRoutes(authorized, adminAuthzHandler)
	auditlogtransport.RegisterAdminRoutes(authorized, adminAuditLogHandler)
	authorized.GET("/authz/permissions/catalog", func(ctx *gin.Context) {
		response.Success(ctx, buildAdminPermissionCatalog(engine))
	})

	// 文件上传
	uploadtransport.RegisterAdminRoutes(authorized, uploadtransport.NewAdminHandler(c.UploadService, c.ContentMediaService))

	// 订单管理
	ordertransport.RegisterAdminRoutes(authorized, adminOrderHandler)
	ordertransport.RegisterAdminRefundWriteRoutes(authorized, adminOrderRefundHandler)
	ordertransport.RegisterAdminRefundRoutes(authorized, adminOrderRefundHandler)
	fulfillmenttransport.RegisterAdminRoutes(authorized, adminFulfillmentHandler)
	cardsecrettransport.RegisterAdminRoutes(authorized, adminCardSecretHandler)
	giftcardtransport.RegisterAdminRoutes(authorized, adminGiftCardHandler)
	invitecodetransport.RegisterAdminRoutes(authorized, invitecodetransport.NewAdminHandler(c.InviteCodeService, c.UserStore))

	// 优惠券与活动价
	coupontransport.RegisterAdminRoutes(authorized, adminCouponHandler)
	promotiontransport.RegisterAdminRoutes(authorized, adminPromotionHandler)

	// 会员等级
	memberleveltransport.RegisterAdminRoutes(authorized, adminMemberLevelHandler)

	// 支付渠道与支付记录
	paymenttransport.RegisterAdminChannelRoutes(authorized, adminPaymentChannelHandler)
	paymenttransport.RegisterAdminRoutes(authorized, adminPaymentHandler)

	// 用户管理
	adminusertransport.RegisterAdminRoutes(authorized, adminUserHandler)
	wallettransport.RegisterAdminRoutes(authorized, adminWalletHandler)
	adminauthtransport.RegisterAdminUser2FARoutes(authorized, adminUser2FAHandler)

	// API 凭证审核管理
	apicredentialtransport.RegisterAdminRoutes(authorized, adminApiCredentialHandler)

	// 站点对接连接管理
	siteconnectiontransport.RegisterAdminRoutes(authorized, siteconnectiontransport.NewAdminHandler(
		c.SiteConnectionService,
		c.ProductMappingService,
	))

	// 商品映射管理
	mappinghttp.RegisterAdminRoutes(authorized, adminCatalogProductMappingHandler)

	// 采购单管理
	procurementtransport.RegisterAdminRoutes(authorized, adminProcurementHandler)

	// 对账管理
	reconciliationtransport.RegisterAdminRoutes(authorized, reconciliationtransport.NewAdminHandler(c.ReconciliationService))

	// Telegram Bot 群发（含逐收件人投递分页与显式安全重试）
	broadcasthttp.RegisterAdminRoutes(authorized, broadcasthttp.NewAdminHandler(c.TelegramBroadcastService))
}
