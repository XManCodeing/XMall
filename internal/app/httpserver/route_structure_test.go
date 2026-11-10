package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/app/container"
	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/logger"
	categorystore "github.com/XManCodeing/XMall/internal/modules/catalog/category/infrastructure/gormstore"
	downstreamstore "github.com/XManCodeing/XMall/internal/modules/downstreamcallback/infrastructure/gormstore"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	paymentapp "github.com/XManCodeing/XMall/internal/modules/payment/application"
	paymentstore "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gormstore"
	siteconnectionstore "github.com/XManCodeing/XMall/internal/modules/siteconnection/infrastructure/gormstore"
	groupsapp "github.com/XManCodeing/XMall/internal/modules/telegram/groups/application"
	runtimeapp "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/application"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/authz"
	admincontract "github.com/XManCodeing/XMall/internal/modules/identity/admin/contract"
	admindomain "github.com/XManCodeing/XMall/internal/modules/identity/admin/domain"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// TestMembershipOrderReadAuthorizationFailsClosed checks all allow/error combinations and safe diagnostics.
// t owns an in-memory logger; no real configuration, database or log files are used.
func TestMembershipOrderReadAuthorizationFailsClosed(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	previous := logger.L
	logger.L = zap.New(core)
	t.Cleanup(func() { logger.L = previous })
	const secret = "synthetic-private-error"
	for _, scenario := range []struct {
		allowed bool
		err     error
		want    bool
	}{
		{true, nil, true}, {false, nil, false}, {true, errors.New(secret), false}, {false, errors.New(secret), false},
	} {
		allowed := enforceMembershipOrderRead(func(id uint, resource, action string) (bool, error) {
			if id != 7 || resource != "/admin/orders/9" || action != "GET" {
				t.Fatal("permission target changed")
			}
			return scenario.allowed, scenario.err
		}, 7, "/admin/orders/9")
		if allowed != scenario.want {
			t.Fatalf("allowed=%v want=%v", allowed, scenario.want)
		}
	}
	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("want two diagnostics, got %d", len(entries))
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if entry.Message != "membership_order_read_authorization_failed" || fields["error_type"] != "*errors.errorString" {
			t.Fatalf("missing safe diagnostic: %v", entry)
		}
		if strings.Contains(fmt.Sprint(fields), secret) {
			t.Fatal("raw error leaked")
		}
	}
}

// TestManualConfirmationProductionRoutes 验证真实路由装配、权限目录及匿名鉴权边界；t 不启动服务或使用真实数据库。
func TestManualConfirmationProductionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := SetupRouter(&config.Config{}, &container.Container{
		TelegramGroups: &groupsapp.Service{}, TelegramBotManager: &runtimeapp.Manager{},
		PaymentService: &paymentapp.PaymentService{}, PaymentStore: &paymentstore.Store{}, PaymentChannelStore: &paymentstore.ChannelStore{},
		CategoryRepo: categorystore.NewCategoryStore(nil), UserStore: userstore.New(nil),
		SiteConnectionRepo: siteconnectionstore.New(nil), DownstreamOrderRefRepo: &downstreamstore.Store{},
	})
	catalog := buildAdminPermissionCatalog(router)
	extracted, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/admin/payments/:id/manual-confirmation"
		found := false
		for _, route := range router.Routes() {
			if route.Method == method && route.Path == path {
				found = true
				if !strings.Contains(route.Handler, "ManualConfirmationHandler") {
					t.Fatalf("manual confirmation delegated to unexpected handler %q", route.Handler)
				}
			}
		}
		if !found {
			t.Fatalf("production route missing: %s %s", method, path)
		}
		found = false
		for _, permission := range catalog {
			if permission.Object == "/admin/payments/:id/manual-confirmation" && permission.Method == method {
				found = true
			}
		}
		if !found {
			t.Fatalf("manual confirmation permission missing from production catalog: %s", method)
		}
		found = false
		for _, route := range extracted {
			if route.object == "/admin/payments/:id/manual-confirmation" && route.method == method {
				found = true
			}
		}
		if !found {
			t.Fatalf("manual confirmation escaped RBAC source coverage: %s", method)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, strings.Replace(path, ":id", "91", 1), nil))
		var envelope struct {
			StatusCode int `json:"status_code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.StatusCode != response.CodeUnauthorized {
			t.Fatalf("anonymous request crossed authentication: status=%d body=%s error=%v", recorder.Code, recorder.Body.String(), err)
		}
	}
}

func TestSetupRouterDelegatesRouteDomains(t *testing.T) {
	routerDirectory := currentRouterDirectory(t)
	source := readRouterSource(t, filepath.Join(routerDirectory, "router.go"))

	for _, registration := range []string{
		"registerStorefrontRoutes(",
		"registerUpstreamRoutes(",
		"registerChannelRoutes(",
		"registerPaymentCallbackRoutes(",
		"registerAdminRoutes(",
	} {
		if !strings.Contains(source, registration) {
			t.Errorf("SetupRouter must delegate through %s", registration)
		}
	}

	for _, inlineGroup := range []string{
		`apiV1.Group("/upstream")`,
		`apiV1.Group("/channel")`,
		`apiV1.Group("/admin")`,
	} {
		if strings.Contains(source, inlineGroup) {
			t.Errorf("router.go must not retain inline domain group %s", inlineGroup)
		}
	}
}

func TestRouteDomainFilesPreserveTrustBoundaries(t *testing.T) {
	routerDirectory := currentRouterDirectory(t)
	tests := []struct {
		file     string
		required []string
	}{
		{
			file: "routes_storefront.go",
			required: []string{
				`storefront.Use(middleware.ResellerTenantMiddleware(`,
				`producthttp.RegisterPublicRoutes(public, publicCatalogHandler)`,
				`categoryhttp.RegisterPublicRoutes(public, publicCategoryHandler)`,
				`contenttransport.RegisterPublicRoutes(public, publicContentHandler)`,
				`captchatransport.RegisterPublicRoutes(public,`,
				`affiliatetransport.RegisterPublicRoutes(public, affiliateHandler)`,
				`affiliatetransport.RegisterUserRoutes(user, affiliateHandler)`,
				`user.Use(middleware.UserJWTAuthMiddleware(`,
				`membershiptransport.RegisterUserRoutes(membershipRoutes, membershiptransport.NewUserHandler(c.MembershipUserAccess))`,
				`Prefix: "user:membership-access"`,
				`resellerConsole.Use(middleware.RequireMainTenantForResellerConsole())`,
				`resellertransport.RegisterUserConsoleRoutes(resellerConsole, userResellerHandler)`,
				`resellertransport.RegisterUserProductSettingRoutes(resellerConsole, userResellerProductSettingHandler)`,
				`resellertransport.RegisterUserFinanceRoutes(resellerConsole, userResellerFinanceHandler)`,
				`resellertransport.RegisterUserOrderRoutes(resellerConsole, userResellerOrderHandler)`,
				`apicredentialtransport.RegisterUserRoutes(user, userApiCredentialHandler)`,
				`auditlogtransport.RegisterUserRoutes(user, userAuditLogHandler)`,
				`giftcardtransport.RegisterUserRoutes(giftCardRedeem, userGiftCardHandler)`,
				`wallettransport.RegisterUserRoutes(user, userWalletHandler)`,
				`userauthtransport.RegisterUserProfileRoutes(user, userProfileHandler)`,
				`userauthtransport.RegisterUserEmailRoutes(user, userEmailHandler)`,
				`userauthtransport.RegisterUserVerifyAuthRoutes(auth, userVerifyHandler, authRateLimit)`,
				`userauthtransport.RegisterUserRegisterAuthRoutes(auth, userLoginHandler, authRateLimit)`,
				`userauthtransport.RegisterUserLoginAuthRoutes(auth, userLoginHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIPAndJSONField("email")))`,
				`userauthtransport.RegisterUser2FAAuthRoutes(auth, user2FAHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIP))`,
				`userauthtransport.RegisterUser2FARoutes(user, user2FAHandler)`,
				`userauthtransport.RegisterUserTelegramAuthRoutes(auth, userTelegramHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIP))`,
				`userauthtransport.RegisterUserTelegramRoutes(user, userTelegramHandler)`,
				`userauthtransport.RegisterUserTelegramOIDCAuthRoutes(auth, userTelegramOIDCHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIP))`,
				`userauthtransport.RegisterUserTelegramOIDCRoutes(user, userTelegramOIDCHandler)`,
				`userauthtransport.RegisterUserGoogleAuthRoutes(auth, userGoogleHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIP))`,
				`userauthtransport.RegisterUserInviteAuthRoutes(auth, userInviteHandler, middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByIP))`,
				`userauthtransport.RegisterUserGoogleRoutes(`,
				`middleware.RateLimitMiddleware(redisClient, loginRule, middleware.KeyByUserIDAndIP)`,
				`userauthtransport.RegisterUserPasswordAuthRoutes(auth, userPasswordHandler, authRateLimit)`,
				`userauthtransport.RegisterUserPasswordRoutes(user, userPasswordHandler)`,
				`memberleveltransport.RegisterPublicRoutes(public, publicMemberLevelHandler)`,
				`publicconfigtransport.RegisterPublicRoutes(public, publicConfigHandler)`,
				`carttransport.RegisterUserRoutes(user, userCartHandler)`,
				`ordertransport.RegisterUserReadRoutes(user, userOrderHandler)`,
				`ordertransport.RegisterUserCancelRoute(user, userOrderHandler)`,
				`ordertransport.RegisterUserPreviewRoute(user, orderPreviewHandler)`,
				`ordertransport.RegisterUserCreateRoute(user, orderCreateHandler)`,
				`ordertransport.RegisterUserCreateAndPayRoute(user, orderCreateHandler)`,
				`ordertransport.RegisterUserPaymentChannelsRoute(user, userOrderHandler)`,
				`ordertransport.RegisterGuestReadRoutes(guestRead, guestOrderHandler)`,
				`guestRead.Use(middleware.RateLimitMiddleware(redisClient, guestReadRule, middleware.KeyByIP))`,
				`ordertransport.RegisterGuestPreviewRoute(guestRead, orderPreviewHandler)`,
				`ordertransport.RegisterGuestCreateRoute(guestWrite, orderCreateHandler)`,
				`ordertransport.RegisterGuestCreateAndPayRoute(guestWrite, orderCreateHandler)`,
				`guestWrite.Use(middleware.RateLimitMiddleware(redisClient, guestWriteRule, middleware.KeyByIP))`,
				`paymenttransport.RegisterGuestWriteRoutes(guestWrite, paymentWriteHandler)`,
				`paymenttransport.RegisterGuestLatestRoute(guestRead, paymentLatestHandler)`,
				`paymenttransport.RegisterUserWriteRoutes(user, paymentWriteHandler)`,
				`paymenttransport.RegisterUserLatestRoute(user, paymentLatestHandler)`,
				`paymenttransport.RegisterWebhookRoutes(callbacks, webhookHandler)`,
			},
		},
		{
			file: "routes_upstream.go",
			required: []string{
				`upstreamAPI.Use(middleware.RateLimitMiddleware(`,
				`upstreamAPI.Use(middleware.UpstreamAPIAuthMiddleware(`,
				`apiV1.POST("/upstream/callback",`,
			},
		},
		{
			file: "routes_channel.go",
			required: []string{
				`channelAPI.Use(middleware.ChannelAPIAuthMiddleware(`,
				`telegramtransport.RegisterChannelBotRoutes(channelAPI, channelTelegramBotHandler)`,
				`affiliatetransport.RegisterChannelRoutes(channelAPI, channelAffiliateHandler)`,
				`memberleveltransport.RegisterChannelRoutes(channelAPI, channelMemberLevelHandler)`,
				`wallettransport.RegisterChannelRoutes(channelAPI, channelWalletHandler)`,
				`giftcardtransport.RegisterChannelRoutes(channelAPI, channelGiftCardHandler)`,
			},
		},
		{
			file: "routes_admin.go",
			required: []string{
				`adminauthtransport.RegisterAdminLoginAuthRoutes(admin, adminLoginHandler, middleware.RateLimitMiddleware(redisClient, adminLoginRule, middleware.KeyByIP))`,
				`adminauthtransport.RegisterAdmin2FAAuthRoutes(admin, admin2FAHandler, middleware.RateLimitMiddleware(redisClient, adminLoginRule, middleware.KeyByIP))`,
				`adminauthtransport.RegisterAdminPasswordRoutes(authorized, adminLoginHandler)`,
				`adminauthtransport.RegisterAdmin2FARoutes(authorized, admin2FAHandler)`,
				`adminauthtransport.RegisterAdminUser2FARoutes(authorized, adminUser2FAHandler)`,
				`adminusertransport.RegisterAdminRoutes(authorized, adminUserHandler)`,
				`adminauthztransport.RegisterAdminRoutes(authorized, adminAuthzHandler)`,
				`ordertransport.RegisterAdminRoutes(authorized, adminOrderHandler)`,
				`ordertransport.RegisterAdminRefundWriteRoutes(authorized, adminOrderRefundHandler)`,
				`ordertransport.RegisterAdminRefundRoutes(authorized, adminOrderRefundHandler)`,
				`fulfillmenttransport.RegisterAdminRoutes(authorized, adminFulfillmentHandler)`,
				`authorized := admin.Use(middleware.JWTAuthMiddleware(`,
				`middleware.AdminRBACMiddleware(c.AuthzService)`,
				`systemtransport.RegisterAdminRoutes(authorized,`,
				`contenttransport.RegisterAdminRoutes(authorized, adminContentHandler)`,
				`dashboardtransport.RegisterAdminRoutes(authorized, adminDashboardHandler)`,
				`memberleveltransport.RegisterAdminRoutes(authorized, adminMemberLevelHandler)`,
				`apicredentialtransport.RegisterAdminRoutes(authorized, adminApiCredentialHandler)`,
				`auditlogtransport.RegisterAdminRoutes(authorized, adminAuditLogHandler)`,
				`cardsecrettransport.RegisterAdminRoutes(authorized, adminCardSecretHandler)`,
				`giftcardtransport.RegisterAdminRoutes(authorized, adminGiftCardHandler)`,
				`settingstransport.RegisterAdminRoutes(authorized, adminSettingsHandler)`,
				`settingstransport.RegisterAdminSMTPRoutes(authorized,`,
				`settingstransport.RegisterAdminCaptchaRoutes(authorized,`,
				`settingstransport.RegisterAdminTelegramAuthRoutes(authorized,`,
				`settingstransport.RegisterAdminGoogleAuthRoutes(authorized,`,
				`settingstransport.RegisterAdminOrderEmailTemplateRoutes(authorized,`,
				`settingstransport.RegisterAdminAffiliateRoutes(authorized,`,
				`settingstransport.RegisterAdminTelegramBotRoutes(authorized,`,
				`uploadtransport.RegisterAdminRoutes(authorized,`,
				`broadcasthttp.RegisterAdminRoutes(authorized,`,
				`siteconnectiontransport.RegisterAdminRoutes(authorized,`,
				`affiliatetransport.RegisterAdminRoutes(authorized, adminAffiliateHandler)`,
				`affiliatetransport.RegisterAdminFinanceRoutes(authorized, adminAffiliateHandler)`,
				`producthttp.RegisterAdminRoutes(authorized, adminCatalogProductHandler)`,
				`categoryhttp.RegisterAdminRoutes(authorized, adminCatalogCategoryHandler)`,
				`mappinghttp.RegisterAdminRoutes(authorized, adminCatalogProductMappingHandler)`,
				`coupontransport.RegisterAdminRoutes(authorized, adminCouponHandler)`,
				`promotiontransport.RegisterAdminRoutes(authorized, adminPromotionHandler)`,
				`notificationtransport.RegisterAdminRoutes(authorized, adminNotificationHandler)`,
				`procurementtransport.RegisterAdminRoutes(authorized, adminProcurementHandler)`,
				`resellertransport.RegisterOperationsOverviewRoutes(authorized, adminResellerOperationsHandler)`,
				`resellertransport.RegisterManagementRoutes(authorized, adminResellerManagementHandler)`,
				`resellertransport.RegisterProfileDetailRoutes(authorized, adminResellerProfileDetailHandler)`,
				`resellertransport.RegisterSiteConfigRoutes(authorized, adminResellerSiteConfigHandler)`,
				`resellertransport.RegisterProductSettingRoutes(authorized, adminResellerProductSettingHandler)`,
				`resellertransport.RegisterOperationsFinanceRoutes(authorized, adminResellerOperationsHandler)`,
				`resellertransport.RegisterFinanceRoutes(authorized, adminResellerFinanceHandler)`,
				`paymenttransport.RegisterAdminChannelRoutes(authorized, adminPaymentChannelHandler)`,
				`paymenttransport.RegisterAdminRoutes(authorized, adminPaymentHandler)`,
				`wallettransport.RegisterAdminRoutes(authorized, adminWalletHandler)`,
				`reconciliationtransport.RegisterAdminRoutes(authorized,`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			source := readRouterSource(t, filepath.Join(routerDirectory, test.file))
			for _, required := range test.required {
				if !strings.Contains(source, required) {
					t.Errorf("%s must preserve trust-boundary statement %q", test.file, required)
				}
			}
		})
	}
}

func TestDashboardAdvertisingRemoved(t *testing.T) {
	root := filepath.Clean(filepath.Join(currentRouterDirectory(t), "..", "..", ".."))
	for _, file := range []string{
		"frontend/admin/src/views/Dashboard.vue",
		"frontend/admin/src/api/admin.ts",
		"frontend/admin/src/api/types.ts",
		"internal/app/container/container.go",
		"internal/app/container/services_application.go",
		"internal/app/httpserver/routes_admin.go",
	} {
		source := readRouterSource(t, filepath.Join(root, file))
		for _, removed := range []string{"DashboardAd", "dashboard_top_banner", "dashboard_kpi_card", "dashboard_sponsored", "renderAdSlot", "reportAdImpression", "AdRender", "/admin/ads", "adproxy", "AdProxyService"} {
			if strings.Contains(source, removed) {
				t.Errorf("%s must not retain advertising reference %q", file, removed)
			}
		}
	}
	component := filepath.Join(root, "frontend/admin/src/components/admin/DashboardAd.vue")
	if _, err := os.Stat(component); !os.IsNotExist(err) {
		t.Errorf("advertising component must stay removed, stat error: %v", err)
	}
	moduleRoot := filepath.Join(root, "internal/modules/adproxy")
	if err := filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Errorf("advertising module source must stay removed: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("check removed advertising module: %v", err)
	}
}

// TestMembershipShareEncryptionIsWired verifies the production router supplies the server-only key.
// t inspects composition source only; no runtime configuration or secrets are read.
func TestMembershipShareEncryptionIsWired(t *testing.T) {
	source := readRouterSource(t, filepath.Join(currentRouterDirectory(t), "router.go"))
	for _, required := range []string{
		"WithMembershipShare(membershipstore.New(gormdb.DB), membershipstore.New(gormdb.DB))",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing encrypted product-share wiring: %s", required)
		}
	}
}

func TestSystemUpdateRepositoryIsWired(t *testing.T) {
	source := readRouterSource(t, filepath.Join(currentRouterDirectory(t), "routes_admin.go"))
	if !strings.Contains(source, "systemtransport.NewAdminHandler(nil, cfg.Update.Repository)") {
		t.Fatal("system updater must receive the configured repository")
	}
}

func currentRouterDirectory(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve router test filename")
	}
	return filepath.Dir(thisFile)
}

func readRouterSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router source %s: %v", path, err)
	}
	return string(raw)
}

// supportRouteContract is the explicit method/template inventory. Both runtime
// registration and static RBAC discovery must match it, not merely a route count.
var supportRouteContract = []string{
	"GET /settings/:bot_id", "PUT /settings/:bot_id",
	"GET /topic-operations", "GET /topic-operations/:id",
	"POST /topic-operations/:id/resolve", "POST /topic-operations/:id/confirm-deleted",
	"GET /upstreams", "GET /upstreams/:id", "POST /upstreams/:id/resolve",
	"POST /upstreams/:id/reconcile", "POST /upstreams/:id/retry",
	"GET /downstreams", "GET /downstreams/:id", "POST /downstreams/:id/resolve",
	"POST /downstreams/:id/reconcile", "POST /downstreams/:id/retry",
	"GET /sessions", "GET /sessions/:id", "GET /sessions/:id/messages",
	"GET /sessions/:id/messages/:message_id/content",
}

// assertSupportRouteInventory requires each registered method/path exactly once.
// router is the protected test router; any missing, duplicate or new route fails.
func assertSupportRouteInventory(t *testing.T, router *gin.Engine) {
	t.Helper()
	actual := map[string]int{}
	for _, route := range router.Routes() {
		actual[route.Method+" "+strings.TrimPrefix(route.Path, "/api/v1/admin/telegram-bot/support")]++
	}
	assertSupportRouteSet(t, actual)
}

// assertSupportSourceInventory verifies static permission coverage sees all routes.
// The scanner is the existing production-route discovery used by this package.
func assertSupportSourceInventory(t *testing.T) {
	t.Helper()
	routes, err := extractAdminRoutesFromSource()
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]int{}
	for _, route := range routes {
		if strings.HasPrefix(route.object, "/admin/telegram-bot/support/") {
			actual[route.method+" "+strings.TrimPrefix(route.object, "/admin/telegram-bot/support")]++
		}
	}
	assertSupportRouteSet(t, actual)
}

// assertSupportRouteSet compares counts and individual identities to the contract.
func assertSupportRouteSet(t *testing.T, actual map[string]int) {
	t.Helper()
	if len(actual) != len(supportRouteContract) {
		t.Fatalf("support route contract changed: got %v", actual)
	}
	for _, route := range supportRouteContract {
		if actual[route] != 1 {
			t.Fatalf("support route missing or duplicated: %s (%d)", route, actual[route])
		}
	}
}

// assertSupportWriteRouteIsolation gives separate admins one exact write policy
// each. Real JWT/RBAC must accept only that method/path, never another recovery
// action. A read-only grant at every same path still cannot perform any write.
// This tests middleware isolation; the real handlers' second exact-permission
// checks are covered by container/application authorization tests, not bypassed.
func assertSupportWriteRouteIsolation(t *testing.T, router *gin.Engine, repo admincontract.Store, auth *authz.Service) {
	t.Helper()
	var writes []string
	for _, route := range supportRouteContract {
		if strings.HasPrefix(route, "POST ") {
			writes = append(writes, strings.TrimPrefix(route, "POST "))
		}
	}
	for index, grant := range append(writes, "read-only") {
		role := "support-exact-" + strings.ReplaceAll(grant, "/", "-")
		admin := &admindomain.Admin{Username: role, PasswordHash: "hash"}
		if err := repo.Create(admin); err != nil {
			t.Fatal(err)
		}
		if grant == "read-only" {
			for _, path := range writes {
				if err := auth.GrantRolePolicy(role, "/admin/telegram-bot/support"+path, "GET"); err != nil {
					t.Fatal(err)
				}
			}
		} else if err := auth.GrantRolePolicy(role, "/admin/telegram-bot/support"+grant, "POST"); err != nil {
			t.Fatal(err)
		}
		if err := auth.SetAdminRoles(admin.ID, []string{role}); err != nil {
			t.Fatal(err)
		}
		token := signContentAdminToken(t, admin)
		for _, path := range writes {
			expected := response.CodeForbidden
			if path == grant {
				expected = response.CodeOK
			}
			request := httptest.NewRequest("POST", "/api/v1/admin/telegram-bot/support"+strings.ReplaceAll(path, ":id", "1"), nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Admin-ID", "1")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			var body struct {
				Status int `json:"status_code"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != expected {
				t.Fatalf("grant %d %s must not authorize %s: %s", index, grant, path, result.Body.String())
			}
		}
	}
}
