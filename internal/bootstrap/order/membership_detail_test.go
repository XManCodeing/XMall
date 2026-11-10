package orderwiring

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/i18n"
	fulfillment "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	externalidentity "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userstore "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	orderhttp "github.com/XManCodeing/XMall/internal/modules/order/transport/http"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	paymentstore "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gormstore"
	reseller "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	membershipcontract "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMembershipStateFiltersBeforePagination verifies HTTP enums, root matching and count/paging.
// The isolated fixture never contacts Telegram or invokes a business writer.
func TestMembershipStateFiltersBeforePagination(t *testing.T) {
	router, db := membershipDetailFixture(t)
	paid := time.Now().UTC().AddDate(0, -3, 0)
	if err := db.Create(&membership.LedgerSource{OrderItemID: 11, TargetID: 21, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, PaidAt: paid, Kind: "months", Months: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		total int
		id    uint
	}{
		{"rights=none&page_size=1", 1, 5}, {"rights=expired&page_size=1", 1, 1},
		{"rights=expired&page_size=1&page=2", 1, 0},
		{"rights=expired&member_removal=unconfirmed&invite_cleanup=none", 1, 1},
		{"rights=expired&member_removal=removed", 0, 0}, {"rights=expired&invite_cleanup=succeeded", 0, 0},
		{"rights=none&order_no=parent", 0, 0},
		{"invite_cleanup=not_required", 0, 0},
	} {
		body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders?"+test.query, true).Body.Bytes()
		var result struct {
			StatusCode int `json:"status_code"`
			Data       []struct {
				ID uint `json:"id"`
			} `json:"data"`
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != 0 || result.Pagination.Total != test.total || (test.id == 0 && len(result.Data) != 0) || (test.id != 0 && (len(result.Data) != 1 || result.Data[0].ID != test.id)) {
			t.Fatalf("%s: %s", test.query, body)
		}
	}
	for _, query := range []string{"rights=bogus", "member_removal=succeeded", "invite_cleanup=removed"} {
		body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders?"+query, true).Body.Bytes()
		var result struct {
			StatusCode int `json:"status_code"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.StatusCode != 400 {
			t.Fatalf("invalid %s: %s %v", query, body, err)
		}
	}
}

// membershipDetailFixture 创建单连接真实仓储/查询适配器/handler，不装配任何交易服务。
// 返回路由和 DB，测试只使用合成身份；无当前商品、群或路由配置表。
func membershipDetailFixture(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "detail.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillment.Fulfillment{}, &userdomain.User{}, &paymentdomain.Payment{}, &membership.OrderItemSnapshot{}, &membership.LedgerSource{}, &membership.LedgerProjection{}, &membership.LedgerRevocation{}, &membership.LedgerOutbox{}, &membership.PaidEvent{}, &membership.IdentityMigration{}, &membership.AccessOperation{}, &membership.AccessInvite{}, &membership.AccessMember{}, &membership.InviteCleanupCycle{}, &membership.InviteCleanupItem{}); err != nil {
		t.Fatal(err)
	}
	parent := uint(1)
	for _, order := range []orderdomain.Order{{ID: 1, OrderNo: "parent", UserID: 7}, {ID: 2, OrderNo: "member-child", ParentID: &parent, UserID: 7}, {ID: 3, OrderNo: "ordinary-child", ParentID: &parent, UserID: 7}, {ID: 4, OrderNo: "ordinary", UserID: 7}, {ID: 5, OrderNo: "direct", UserID: 7}} {
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []orderdomain.OrderItem{{ID: 11, OrderID: 2}, {ID: 12, OrderID: 3}, {ID: 13, OrderID: 4}, {ID: 14, OrderID: 5}} {
		item.ProductID, item.SKUID, item.Quantity = 88, 99, 1
		item.TitleJSON = jsonmap.JSON{"en-US": "Synthetic item"}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint{11, 14} {
		snapshot := membership.OrderItemSnapshot{OrderItemID: id, ProductID: 88, SKUID: 99, AccountID: 7, BeneficiaryTelegramID: 9007199254740993, IdentityVersion: 8, ConfigVersion: 9, Kind: "months", Months: 18, DurationSeconds: 46656000, Quantity: 1, CreatedAt: time.Now().UTC(), Targets: []membership.SnapshotTarget{{TargetID: 21, BotInstanceID: 31, RouteVersion: 41, TelegramChatID: -1009007199254740993}}}
		if id == 14 {
			snapshot.Kind, snapshot.Months, snapshot.DurationSeconds = "permanent", 0, 0
		}
		if err := db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := orderstore.New(db, "synthetic-test-only")
	handler := orderhttp.NewAdminHandler(orderAdminQueryAdapter{readonly: store}, orderAdminUserAdapter{users: userstore.New(db)}, orderAdminCouponAdapter{}, orderAdminPromotionAdapter{}, orderAdminPaymentAdapter{payments: paymentstore.New(db, "synthetic-test-only")}, orderAdminPaymentChannelAdapter{}).
		WithMembership(orderAdminQueryAdapter{readonly: store.MembershipOnly()}, membershipstore.New(db)).WithMembershipLedger(membershipstore.New(db))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	// 与生产一样在共用 authorized 路由组上安装门卫，验证新呈现不绕过中间件。
	group := router.Group("/admin", func(c *gin.Context) {
		if c.GetHeader("X-Test-Authorized") != "yes" {
			c.AbortWithStatus(http.StatusForbidden)
		}
	})
	orderhttp.RegisterAdminRoutes(group, handler)
	orderhttp.RegisterMembershipRoutes(group, handler)
	userHandler := orderhttp.NewUserHandler(membershipUserQuery{store: store}, nil, nil, nil).
		WithMembershipSnapshots(membershipstore.New(db))
	userGroup := router.Group("/user", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 32)
		if err != nil || id == 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("user_id", uint(id))
	})
	orderhttp.RegisterUserReadRoutes(userGroup, userHandler)
	return router, db
}

// membershipUserQuery 复用真实带账号范围的仓储；嵌入端口的其他方法不参与本详情测试。
type membershipUserQuery struct {
	orderhttp.UserOrderQuery
	store *orderstore.Store
}

// GetOrderByUserOrderNoForTenant 返回本账号主站订单，保留生产查询的范围及父项聚合语义。
func (query membershipUserQuery) GetOrderByUserOrderNoForTenant(_ reseller.TenantContext, orderNo string, uid uint) (*orderdomain.Order, error) {
	order, err := query.store.GetByOrderNoAndUserScoped(orderNo, uid, ordercontract.TenantScope{})
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, orderhttp.ErrOrderNotFound
	}
	orderapp.FillOrderItemsFromChildren(order)
	return order, nil
}

// TestUserMembershipDetail 验证已授权网站详情的最小快照、精度、混合项及普通字段隔离。
func TestUserMembershipDetail(t *testing.T) {
	router, db := membershipDetailFixture(t)
	requestDetail := func(name, user string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/user/orders/"+name, nil)
		request.Header.Set("X-Test-User", user)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Body.String()
	}
	for _, name := range []string{"parent", "direct"} {
		body := requestDetail(name, "7")
		if !strings.Contains(body, `"beneficiary_telegram_id":"9007199254740993"`) || !strings.Contains(body, `"config_version":9`) {
			t.Fatalf("missing immutable purchase snapshot: %s", body)
		}
		for _, secret := range []string{"telegram_chat_id", "bot_instance_id", "identity_version", "target_id", "duration_seconds"} {
			if strings.Contains(body, secret) {
				t.Fatalf("internal field exposed: %s", secret)
			}
		}
		if name == "parent" && (strings.Count(body, `"membership_snapshot"`) != 2 || !strings.Contains(body, `"months":18`)) {
			t.Fatalf("parent/child or mixed item projection: %s", body)
		}
		if name == "direct" && !strings.Contains(body, `"kind":"permanent"`) {
			t.Fatalf("permanent term missing: %s", body)
		}
	}
	if body := requestDetail("parent", "8"); !strings.Contains(body, `"status_code":404`) || strings.Contains(body, "membership_snapshot") {
		t.Fatalf("other account read: %s", body)
	}
	if body := requestDetail("ordinary", "7"); strings.Contains(body, "membership_snapshot") {
		t.Fatalf("ordinary item polluted: %s", body)
	}
	if err := db.Model(&membership.OrderItemSnapshot{}).Where("order_item_id = ?", 11).Update("account_id", 8).Error; err != nil {
		t.Fatal(err)
	}
	if body := requestDetail("parent", "7"); !strings.Contains(body, `"status_code":500`) || strings.Contains(body, "membership_snapshot") {
		t.Fatalf("inconsistent ownership accepted: %s", body)
	}
	if err := db.Model(&membership.OrderItemSnapshot{}).Where("order_item_id = ?", 11).Updates(map[string]interface{}{"account_id": 7, "targets": "invalid-json"}).Error; err != nil {
		t.Fatal(err)
	}
	if body := requestDetail("parent", "7"); !strings.Contains(body, `"status_code":500`) || strings.Contains(body, "membership_snapshot") {
		t.Fatalf("corrupt snapshot returned partial detail: %s", body)
	}
	request := httptest.NewRequest(http.MethodGet, "/user/orders/direct", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "membership_snapshot") {
		t.Fatal("unauthenticated snapshot disclosure")
	}
}

// membershipPreviewFailure 返回被包装的领域错误，验证 HTTP 映射保留 errors.Is 语义。
type membershipPreviewFailure struct{ failure error }

// PreviewOrder 模拟登录预览拒绝，不创建任何订单。
func (stub membershipPreviewFailure) PreviewOrder(orderhttp.CreateOrderInput) (*orderhttp.OrderPreview, error) {
	return nil, fmt.Errorf("synthetic private diagnostic: %w", stub.failure)
}

// PreviewGuestOrder 模拟游客预览拒绝，沿用相同业务错误。
func (stub membershipPreviewFailure) PreviewGuestOrder(orderhttp.CreateGuestOrderInput) (*orderhttp.OrderPreview, error) {
	return nil, fmt.Errorf("synthetic private diagnostic: %w", stub.failure)
}

// TestMembershipPurchaseErrors 验证购买错误为可行动三语业务消息，不泄漏内部错误或误报服务器故障。
func TestMembershipPurchaseErrors(t *testing.T) {
	cases := []struct {
		failure error
		key     string
	}{
		{membershipcontract.ErrPermanentFullyCovered, "error.membership_permanent_covered"},
		{membershipcontract.ErrPermanentPartiallyCovered, "error.membership_permanent_partial"},
		{membershipcontract.ErrSalesClosed, "error.membership_sales_closed"},
		{membershipcontract.ErrUntrustedIdentity, "error.membership_binding_required"},
		{membershipcontract.ErrConflict, "error.membership_purchase_changed"},
		{membershipcontract.ErrCapability, "error.membership_purchase_unavailable"},
		{membershipcontract.ErrOverlapUnavailable, "error.membership_purchase_unavailable"},
		{membershipcontract.ErrInvalid, "error.membership_purchase_invalid"},
	}
	for _, example := range cases {
		for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
			router := gin.New()
			handler := orderhttp.NewPreviewHandler(membershipPreviewFailure{failure: example.failure})
			user := router.Group("/user", func(c *gin.Context) { c.Set("user_id", uint(7)) })
			orderhttp.RegisterUserPreviewRoute(user, handler)
			orderhttp.RegisterGuestPreviewRoute(router.Group("/guest"), handler)
			for _, path := range []string{"/user/orders/preview", "/guest/orders/preview"} {
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"synthetic@example.test","order_password":"synthetic-only","items":[{"product_id":1,"quantity":1}]}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept-Language", locale)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				var result struct {
					Code    int    `json:"status_code"`
					Message string `json:"msg"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Code != 400 || result.Message != i18n.T(locale, example.key) || strings.Contains(recorder.Body.String(), "synthetic private") {
					t.Fatalf("%s %s: %s", locale, path, recorder.Body.String())
				}
			}
		}
	}
}

// membershipDetailRequest 调用真实 HTTP handler，返回原始响应供精度与字段缺省断言。
func membershipDetailRequest(t *testing.T, router *gin.Engine, path string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if authorized {
		request.Header.Set("X-Test-Authorized", "yes")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestAdminOrderListsUseDisjointSnapshotScopes 验证真实 handler 的列表总数、分页和混单隔离。
// t 复用只读 SQLite 路由夹具；普通列表不改变会员详情或双方权限门卫。
func TestAdminOrderListsUseDisjointSnapshotScopes(t *testing.T) {
	router, _ := membershipDetailFixture(t)
	for _, test := range []struct {
		path  string
		total int
		id    uint
	}{
		{path: "/admin/orders?page=1&page_size=1", total: 1, id: 4},
		{path: "/admin/orders?order_no=parent", total: 0},
		{path: "/admin/orders?order_no=direct", total: 0},
		{path: "/admin/telegram-bot/membership/orders?page=1&page_size=1", total: 2, id: 5},
		{path: "/admin/telegram-bot/membership/orders?page=2&page_size=1", total: 2, id: 1},
		{path: "/admin/telegram-bot/membership/orders?page=3&page_size=1", total: 2},
		{path: "/admin/telegram-bot/membership/orders?order_no=member-child", total: 0},
		{path: "/admin/telegram-bot/membership/orders?order_no=parent", total: 1, id: 1},
	} {
		recorder := membershipDetailRequest(t, router, test.path, true)
		var result struct {
			StatusCode int `json:"status_code"`
			Data       []struct {
				ID               uint                                      `json:"id"`
				MembershipGroups []membershipcontract.AdminOrderGroupState `json:"membership_groups"`
			} `json:"data"`
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != 0 || result.Pagination.Total != test.total {
			t.Fatalf("%s: %s", test.path, recorder.Body.String())
		}
		if test.id == 0 && len(result.Data) != 0 || test.id != 0 && (len(result.Data) != 1 || result.Data[0].ID != test.id) {
			t.Fatalf("%s: unexpected rows: %s", test.path, recorder.Body.String())
		}
		// Membership facts must remain scoped to the authorized root, including child items.
		for _, row := range result.Data {
			if strings.Contains(test.path, "/membership/") {
				if len(row.MembershipGroups) != 1 || row.MembershipGroups[0].BeneficiaryTelegramID != "9007199254740993" || row.MembershipGroups[0].Rights != "none" {
					t.Fatalf("missing or fabricated membership facts: %s", recorder.Body.String())
				}
			} else if len(row.MembershipGroups) != 0 {
				t.Fatalf("ordinary list leaked membership evidence: %s", recorder.Body.String())
			}
		}
		if denied := membershipDetailRequest(t, router, test.path, false); denied.Code != http.StatusForbidden {
			t.Fatalf("list authorization bypass: %s", test.path)
		}
	}
}

// TestMembershipTelegramSearchHTTP keeps filtering server-side and returns immutable numeric IDs as strings.
// t supplies an isolated single-connection database; no current binding is required for ID searches.
func TestMembershipTelegramSearchHTTP(t *testing.T) {
	router, db := membershipDetailFixture(t)
	if err := db.AutoMigrate(&externalidentity.Identity{}); err != nil {
		t.Fatal(err)
	}
	identity := externalidentity.Identity{UserID: 7, Provider: "telegram", ProviderUserID: "9007199254740993", Username: "SyntheticBuyer"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	for _, example := range []struct {
		keyword string
		total   int
	}{
		{"9007199254740993", 2}, {"@syntheticbuyer", 2}, {"SYNTHETICBUYER", 2}, {"9007199254740992", 0}, {"missingbuyer", 0},
	} {
		path := "/admin/telegram-bot/membership/orders?page_size=1&user_keyword=" + example.keyword
		recorder := membershipDetailRequest(t, router, path, true)
		var result struct {
			StatusCode int               `json:"status_code"`
			Data       []json.RawMessage `json:"data"`
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != 0 || result.Pagination.Total != example.total || len(result.Data) > 1 {
			t.Fatalf("server-side identity search %q: %s", example.keyword, recorder.Body.String())
		}
	}
}

// TestMembershipOrderListRecalculatesExpiry derives expiry directly from dated grant evidence.
// t uses only synthetic ledger facts; listing must not alter transaction status or write lifecycle work.
func TestMembershipOrderListRecalculatesExpiry(t *testing.T) {
	router, db := membershipDetailFixture(t)
	paidAt := time.Now().UTC().AddDate(0, -3, 0)
	source := membership.LedgerSource{OrderItemID: 11, TargetID: 21, Operation: "grant", BeneficiaryTelegramID: 9007199254740993, AccountID: 7, OrderID: 2, PaymentOrderID: 1, PaymentID: 99, PaidAt: paidAt, Kind: "months", Months: 1, SnapshotHash: "synthetic", CreatedAt: paidAt}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&orderdomain.Order{}).Where("id = ?", 1).Update("status", "delivered").Error; err != nil {
		t.Fatal(err)
	}
	body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders?order_no=parent", true).Body.String()
	var result struct {
		StatusCode int `json:"status_code"`
		Data       []struct {
			Status string                                    `json:"status"`
			Groups []membershipcontract.AdminOrderGroupState `json:"membership_groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 0 || len(result.Data) != 1 || result.Data[0].Status != "delivered" || len(result.Data[0].Groups) != 1 {
		t.Fatalf("unexpected scoped order: %s", body)
	}
	group := result.Data[0].Groups[0]
	if group.Rights != "expired" || group.ExpiresAt == nil || group.ExpiresAt.After(time.Now()) || group.MemberRemoval != "unconfirmed" || group.InviteCleanup != "none" {
		t.Fatalf("expiry must not fabricate removal or cleanup: %s", body)
	}
	var count int64
	if err := db.Model(&membership.AccessOperation{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("read endpoint wrote lifecycle work: count=%d err=%v", count, err)
	}
}

// TestMembershipDetailSnapshots 验证父聚合、子项和直接订单均输出不可变身份、期限及路由。
func TestMembershipDetailSnapshots(t *testing.T) {
	router, db := membershipDetailFixture(t)
	before, err := orderstore.New(db, "synthetic-test-only").GetByID(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{1, 2, 5} {
		body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders/"+strconv.Itoa(int(id)), true).Body.String()
		var result struct {
			Code int `json:"code"`
			Data struct {
				Items []struct {
					Snapshot *membership.OrderItemSnapshot `json:"membership_snapshot"`
				} `json:"items"`
				Children []struct {
					Items []struct {
						Snapshot *membership.OrderItemSnapshot `json:"membership_snapshot"`
					} `json:"items"`
				} `json:"children"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Data.Items) == 0 {
			t.Fatalf("missing detail: %s", body)
		}
		snapshot := result.Data.Items[0].Snapshot
		if snapshot == nil || snapshot.AccountID != 7 || snapshot.BeneficiaryTelegramID != 9007199254740993 || snapshot.IdentityVersion != 8 || snapshot.ConfigVersion != 9 || snapshot.Quantity != 1 {
			t.Fatalf("identity/version: %s", body)
		}
		if id == 5 {
			if snapshot.Kind != "permanent" || snapshot.Months != 0 || snapshot.DurationSeconds != 0 {
				t.Fatalf("permanent: %s", body)
			}
		} else if snapshot.Kind != "months" || snapshot.Months != 18 || snapshot.DurationSeconds != 46656000 {
			t.Fatalf("term: %s", body)
		}
		if len(snapshot.Targets) != 1 || snapshot.Targets[0] != (membership.SnapshotTarget{TargetID: 21, BotInstanceID: 31, RouteVersion: 41, TelegramChatID: -1009007199254740993}) {
			t.Fatalf("route: %s", body)
		}
		if !strings.Contains(body, `"beneficiary_telegram_id":"9007199254740993"`) || !strings.Contains(body, `"telegram_chat_id":"-1009007199254740993"`) {
			t.Fatalf("precision: %s", body)
		}
		if id == 1 && (len(result.Data.Children) != 2 || !reflect.DeepEqual(result.Data.Children[0].Items[0].Snapshot, snapshot) || result.Data.Children[1].Items[0].Snapshot != nil || result.Data.Items[1].Snapshot != nil) {
			t.Fatalf("child/mixed scope: %s", body)
		}
	}
	after, err := orderstore.New(db, "synthetic-test-only").GetByID(1)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read changed order: %v", err)
	}
}

// TestMembershipDetailIsolation 验证普通 DTO 不变、非会员拒绝、权限中间件及存储失败关闭。
func TestMembershipDetailIsolation(t *testing.T) {
	router, db := membershipDetailFixture(t)
	for _, id := range []string{"1", "2", "5"} {
		body := membershipDetailRequest(t, router, "/admin/orders/"+id, true).Body.String()
		if strings.Contains(body, "membership_snapshot") || strings.Contains(body, "beneficiary_telegram_id") {
			t.Fatalf("ordinary DTO polluted: %s", body)
		}
	}
	for _, id := range []string{"3", "4", "999"} {
		body := membershipDetailRequest(t, router, "/admin/telegram-bot/membership/orders/"+id, true).Body.String()
		if !strings.Contains(body, `"status_code":404`) || strings.Contains(body, "membership_snapshot") {
			t.Fatalf("scope leak: %s", body)
		}
	}
	path := "/admin/telegram-bot/membership/orders/1"
	if recorder := membershipDetailRequest(t, router, path, false); recorder.Code != http.StatusForbidden || strings.Contains(recorder.Body.String(), "membership_snapshot") {
		t.Fatal("authorization bypass")
	}
	// 保留筛选用主键但损坏 JSON，证明读取失败不吞掉错误或输出部分详情。
	if err := db.Model(&membership.OrderItemSnapshot{}).Where("order_item_id = ?", 11).Update("targets", "invalid-json").Error; err != nil {
		t.Fatal(err)
	}
	body := membershipDetailRequest(t, router, path, true).Body.String()
	if !strings.Contains(body, `"status_code":500`) || strings.Contains(body, "membership_snapshot") {
		t.Fatalf("partial snapshot response: %s", body)
	}
}
