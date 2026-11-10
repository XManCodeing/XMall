package application

import (
	"context"
	"errors"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	identitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	groupsdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	membershipstore "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"gorm.io/gorm"
	"testing"

	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	membershipdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// TestPublicSaleBlocksAllOrderEntrypoints 验证关闭配置仍阻止用户、游客和绕过风控入口。
func TestPublicSaleBlocksAllOrderEntrypoints(t *testing.T) {
	fixture := setupWholesaleOrderFixture(t, "membership_gate", nil, nil, nil)
	if err := fixture.db.AutoMigrate(&membershipdomain.ProductConfig{}); err != nil {
		t.Fatal(err)
	}
	fixture.svc.orderStore = orderstore.New(fixture.db, "test-membership-secret")
	fixture.svc.enforcePublicSale = true
	items := []CreateOrderItem{{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 1}}
	if _, err := fixture.svc.PreviewOrder(CreateOrderInput{UserID: 1, Items: items}); err != nil {
		t.Fatalf("ordinary product rejected: %v", err)
	}
	if err := fixture.db.Create(&membershipdomain.ProductConfig{ProductID: fixture.product.ID, Enabled: false}).Error; err != nil {
		t.Fatal(err)
	}
	user := CreateOrderInput{UserID: 1, Items: items, SkipRiskControl: true, SkipIPRiskControl: true}
	guest := CreateGuestOrderInput{Email: "guest@example.com", OrderPassword: "password123", Items: items}
	checks := map[string]func() error{
		"user preview":  func() error { _, err := fixture.svc.PreviewOrder(user); return err },
		"user create":   func() error { _, err := fixture.svc.CreateOrder(user); return err },
		"guest preview": func() error { _, err := fixture.svc.PreviewGuestOrder(guest); return err },
		"guest create":  func() error { _, err := fixture.svc.CreateGuestOrder(guest); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, productcontract.ErrMembershipSaleClosed) {
				t.Fatalf("expected membership rejection, got %v", err)
			}
		})
	}
}

// testCheckoutStore 仅在白盒测试中替代门控结果和未实现账本；生产没有此通路。
type testCheckoutStore struct {
	ordercontract.Store
	db          *gorm.DB
	before      func(*gorm.DB) error
	beforeWrite func(*gorm.DB) error
	calls       int
	fail        bool
}
type testCheckoutTx struct {
	ordercontract.Transaction
	checkout membership.Checkout
}
type testSaleProducts struct{ productcontract.Repository }

func (testSaleProducts) CheckPublicSale([]uint) error { return nil }
func (tx testCheckoutTx) Products() productcontract.Repository {
	return testSaleProducts{tx.Transaction.Products()}
}
func (tx testCheckoutTx) MembershipCheckout() (membership.Checkout, error) { return tx.checkout, nil }
func (store *testCheckoutStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	store.calls++
	if store.calls == 2 && store.beforeWrite != nil {
		if err := store.beforeWrite(store.db); err != nil {
			return err
		}
	}
	return store.db.Transaction(func(db *gorm.DB) error {
		if store.before != nil {
			if err := store.before(db); err != nil {
				return err
			}
			store.before = nil
		}
		checkout, err := membershipstore.CheckoutInTransaction(db, testOverlap{})
		if err != nil {
			return err
		}
		if err := fn(testCheckoutTx{orderstore.UseTransaction(db, "membership-test"), checkout}); err != nil {
			return err
		}
		if store.fail && hasOrders(db) {
			return errors.New("forced checkout rollback")
		}
		return nil
	})
}

func hasOrders(db *gorm.DB) bool {
	var count int64
	return db.Model(&orderdomain.Order{}).Count(&count).Error == nil && count > 0
}

type testOverlap struct{}

func (testOverlap) CheckPermanentOverlap(context.Context, uint, int64, []membershipdomain.SnapshotTarget) error {
	return nil
}

type testCheckoutQueue struct{}

func (testCheckoutQueue) Enabled() bool                                  { return true }
func (testCheckoutQueue) EnqueueTimeoutCancel(uint, time.Duration) error { return nil }
func (testCheckoutQueue) EnqueueStatusEmail(uint, string) error          { return nil }

// setupMembershipOrder 构造 MaxOpenConns=1 的共享真实 CreateOrder fixture，不改变生产门控。
func setupMembershipOrder(t *testing.T) (wholesaleOrderFixture, *testCheckoutStore) {
	t.Helper()
	fixture := setupWholesaleOrderFixture(t, "membership_create", nil, nil, nil)
	db := fixture.db
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
	if err := db.AutoMigrate(&fulfillmentdomain.Fulfillment{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &identitydomain.Identity{}, &groupsdomain.Target{}, &membershipdomain.ProductShareCode{}, &membershipdomain.ProductConfig{}, &membershipdomain.ProductTarget{}, &membershipdomain.SKUTerm{}, &membershipdomain.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []any{
		&userdomain.User{ID: 7, Email: "membership-test@example.com", Status: constants.UserStatusActive},
		&identitydomain.Identity{ID: 4, UserID: 7, Provider: constants.UserOAuthProviderTelegram, ProviderUserID: "9007199254740993"},
		&groupsdomain.Target{ID: 1, Name: "members", Purpose: "membership", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 9, TelegramChatID: -10001, OccupancySlot: "membership:1", Observation: groupsdomain.Observation{RouteVersion: 1, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, State: "ready", ObservedAt: &now}},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&productdomain.Product{}).Where("id = ?", fixture.product.ID).Update("fulfillment_type", constants.FulfillmentTypeManual).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := membershipstore.New(db).SaveConfig(context.Background(), membership.ConfigInput{ProductID: fixture.product.ID, Quantity: 1, TargetIDs: []uint{1}, Terms: []membershipdomain.SKUTerm{{SKUID: fixture.sku.ID, Kind: membershipdomain.TermMonths, Months: 5}}}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&productdomain.ProductSKU{}).Where("id = ?", fixture.sku.ID).Update("manual_stock_total", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&membershipdomain.ProductConfig{}).Where("product_id = ?", fixture.product.ID).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	store := &testCheckoutStore{Store: orderstore.New(db, "membership-test"), db: db}
	fixture.svc.orderStore = store
	fixture.svc.productRepo = testSaleProducts{fixture.svc.productRepo}
	fixture.svc.enforcePublicSale = true
	fixture.svc.queueClient = testCheckoutQueue{}
	return fixture, store
}

// externalPreflightProbe 在实际购买入口验证事务外端口先于任意交易事务调用。
type externalPreflightProbe struct {
	db    *gorm.DB
	calls int
}

func (p *externalPreflightProbe) Checkout(ctx context.Context, lines []membership.CheckoutLine) (*membership.CapabilityProof, error) {
	p.calls++
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var count int64
	if err := p.db.WithContext(ctx).Model(&groupsdomain.Target{}).Count(&count).Error; err != nil {
		return nil, err
	}
	return &membership.CapabilityProof{Versions: map[uint]uint64{lines[0].ProductID: 1}, Routes: map[uint]uint64{1: 1}}, nil
}
func (*externalPreflightProbe) Payment(context.Context, *orderdomain.Order) error { return nil }

// TestMembershipExternalPreflightWiring 验证生产同一 setter 的实际购买接线及交易内最终校验。
func TestMembershipExternalPreflightWiring(t *testing.T) {
	fixture, store := setupMembershipOrder(t)
	probe := &externalPreflightProbe{db: fixture.db}
	fixture.svc.SetMembershipPreflight(probe)
	store.before = func(*gorm.DB) error {
		if probe.calls != 1 {
			return errors.New("transaction began before external preflight")
		}
		return nil
	}
	if _, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}}}); err != nil {
		t.Fatal(err)
	}
	if probe.calls != 1 {
		t.Fatal(probe.calls)
	}
}

// TestMembershipActualCreatePath 验证门后真实 CreateOrder 父子单持久化与不可变快照，不伪造 Bot source。
func TestMembershipActualCreatePath(t *testing.T) {
	fixture, _ := setupMembershipOrder(t)
	input := CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}}}
	if _, err := fixture.svc.PreviewOrder(input); err != nil {
		t.Fatal(err)
	}
	order, err := fixture.svc.CreateOrder(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(order.Children) != 1 || len(order.Items) != 1 {
		t.Fatalf("unexpected split: %+v", order)
	}
	snapshot, err := membershipstore.New(fixture.db).Snapshot(context.Background(), order.Items[0].ID)
	if err != nil || snapshot.AccountID != 7 || snapshot.Months != 5 || snapshot.BeneficiaryTelegramID != 9007199254740993 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	var count int64
	if err := fixture.db.Model(&membershipdomain.OrderItemSnapshot{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("snapshot count %d %v", count, err)
	}
	// 生产公共门控允许已启用会员；下架后必须拒绝新交易。
	fixture.svc.productRepo = fixture.svc.productRepo.(testSaleProducts).Repository
	if _, err := fixture.svc.CreateOrder(input); err != nil {
		t.Fatalf("enabled production sale: %v", err)
	}
	if err := fixture.db.Model(&membershipdomain.ProductConfig{}).Where("product_id = ?", fixture.product.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.svc.CreateOrder(input); !errors.Is(err, productcontract.ErrMembershipSaleClosed) && !errors.Is(err, membership.ErrSalesClosed) {
		t.Fatalf("gate bypass: %v", err)
	}
}

// TestMembershipCreateRejectsDuplicatesAndRollback 覆盖重复输入行和单连接订单/快照共同回滚。
func TestMembershipCreateRejectsDuplicatesAndRollback(t *testing.T) {
	fixture, store := setupMembershipOrder(t)
	line := CreateOrderItem{ProductID: fixture.product.ID, Quantity: 1}
	if _, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{line, line}}); !errors.Is(err, membership.ErrInvalid) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	store.fail = true
	if _, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{line}}); err == nil {
		t.Fatal("rollback accepted")
	}
	for _, model := range []any{&orderdomain.Order{}, &orderdomain.OrderItem{}, &membershipdomain.OrderItemSnapshot{}} {
		var count int64
		if err := fixture.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rollback leaked %T: %d %v", model, count, err)
		}
	}
}

// TestMembershipPreflightChanges 在真实预检和写订单事务之间模拟已提交的换绑/禁用/配置路由变化。
func TestMembershipPreflightChanges(t *testing.T) {
	cases := map[string]func(*gorm.DB) error{
		"disabled": func(db *gorm.DB) error {
			return db.Model(&userdomain.User{}).Where("id = ?", 7).Update("status", "disabled").Error
		},
		"deleted": func(db *gorm.DB) error {
			return db.Model(&userdomain.User{}).Where("id = ?", 7).Update("deleted_at", time.Now()).Error
		},
		"rebound": func(db *gorm.DB) error {
			return db.Model(&identitydomain.Identity{}).Where("id = ?", 4).Update("user_id", 8).Error
		},
		"version": func(db *gorm.DB) error {
			return db.Model(&membershipdomain.ProductConfig{}).Where("product_id > 0").Update("version", 2).Error
		},
		"route": func(db *gorm.DB) error {
			return db.Model(&groupsdomain.Target{}).Where("id = ?", 1).Updates(map[string]any{"route_version": 2, "obs_route_version": 2}).Error
		},
		"stale": func(db *gorm.DB) error {
			return db.Model(&groupsdomain.Target{}).Where("id = ?", 1).Update("obs_observed_at", time.Now().Add(-time.Hour)).Error
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			fixture, store := setupMembershipOrder(t)
			store.beforeWrite = change
			if _, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}}}); err == nil {
				t.Fatal("stale preflight accepted")
			}
			var count int64
			if err := fixture.db.Model(&orderdomain.Order{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("order leaked %d %v", count, err)
			}
		})
	}
}

// TestMembershipMixedOrderHistory 验证普通商品数量/拆单保留，配置修改只影响新订单。
func TestMembershipMixedOrderHistory(t *testing.T) {
	fixture, _ := setupMembershipOrder(t)
	ordinary := fixture.product
	ordinary.ID = 0
	ordinary.Slug = "ordinary-mixed"
	ordinary.FulfillmentType = constants.FulfillmentTypeManual
	if err := fixture.db.Omit("Category", "SKUs").Create(&ordinary).Error; err != nil {
		t.Fatal(err)
	}
	sku := fixture.sku
	sku.ID = 0
	sku.ProductID = ordinary.ID
	sku.ManualStockTotal = 100
	if err := fixture.db.Create(&sku).Error; err != nil {
		t.Fatal(err)
	}
	order, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}, {ProductID: ordinary.ID, Quantity: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(order.Children) != 2 || len(order.Items) != 2 {
		t.Fatalf("mixed order lost items: %+v", order)
	}
	var count int64
	if err := fixture.db.Model(&membershipdomain.OrderItemSnapshot{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("mixed snapshots %d %v", count, err)
	}
	if _, err := membershipstore.New(fixture.db).SaveConfig(context.Background(), membership.ConfigInput{ProductID: fixture.product.ID, ExpectedVersion: 1, Enabled: true, Quantity: 1, TargetIDs: []uint{1}, Terms: []membershipdomain.SKUTerm{{SKUID: fixture.sku.ID, Kind: membershipdomain.TermMonths, Months: 18}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, item := range order.Items {
		if item.ProductID == ordinary.ID {
			if item.Quantity != 3 {
				t.Fatal("ordinary quantity changed")
			}
			continue
		}
		snapshot, err := membershipstore.New(fixture.db).Snapshot(context.Background(), item.ID)
		if err != nil || snapshot.Months != 5 || snapshot.ConfigVersion != 1 {
			t.Fatalf("history mutated %+v %v", snapshot, err)
		}
	}
}

// TestMembershipDifferentSKUDuplicate 验证同商品不同 SKU 仍不能绕过每单一份。
func TestMembershipDifferentSKUDuplicate(t *testing.T) {
	fixture, _ := setupMembershipOrder(t)
	sku := fixture.sku
	sku.ID = 0
	sku.SKUCode = "OTHER"
	sku.ManualStockTotal = 100
	if err := fixture.db.Create(&sku).Error; err != nil {
		t.Fatal(err)
	}
	_, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, SKUID: fixture.sku.ID, Quantity: 1}, {ProductID: fixture.product.ID, SKUID: sku.ID, Quantity: 1}}})
	if !errors.Is(err, membership.ErrInvalid) {
		t.Fatalf("different SKU duplicate: %v", err)
	}
}

// TestOrdinaryGuestNeedsNoTelegramTables 验证普通游客完整下单不访问绑定/群目标表。
func TestOrdinaryGuestNeedsNoTelegramTables(t *testing.T) {
	fixture, _ := setupMembershipOrder(t)
	if err := fixture.db.Where("product_id = ?", fixture.product.ID).Delete(&membershipdomain.ProductConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Migrator().DropTable(&identitydomain.Identity{}, &groupsdomain.Target{}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&productdomain.Product{}).Where("id = ?", fixture.product.ID).Update("purchase_type", constants.ProductPurchaseGuest).Error; err != nil {
		t.Fatal(err)
	}
	fixture.svc.productRepo = fixture.svc.productRepo.(testSaleProducts).Repository
	fixture.svc.orderStore = orderstore.New(fixture.db, "membership-test")
	order, err := fixture.svc.CreateGuestOrder(CreateGuestOrderInput{Email: "ordinary-guest@example.com", OrderPassword: "test-password", Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 3}}})
	if err != nil || len(order.Items) != 1 || order.Items[0].Quantity != 3 {
		t.Fatalf("ordinary regression: %+v %v", order, err)
	}
}

// TestMembershipConcurrentRebind 用真实单连接和并发换绑阻塞点验证预检后绑定变化不会产生订单。
func TestMembershipConcurrentRebind(t *testing.T) {
	fixture, store := setupMembershipOrder(t)
	prepared := make(chan struct{})
	rebound := make(chan struct{})
	completed := make(chan error, 1)
	store.beforeWrite = func(_ *gorm.DB) error {
		close(prepared)
		select {
		case <-rebound:
			return nil
		case <-time.After(3 * time.Second):
			return errors.New("rebind synchronization timed out")
		}
	}
	go func() {
		_, err := fixture.svc.CreateOrder(CreateOrderInput{UserID: 7, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}}})
		completed <- err
	}()
	select {
	case <-prepared:
	case <-time.After(3 * time.Second):
		t.Fatal("preflight did not finish")
	}
	updateErr := fixture.db.Model(&identitydomain.Identity{}).Where("id = ?", 4).Update("id", 5).Error
	close(rebound)
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("concurrent rebind accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkout deadlocked")
	}
	var count int64
	if err := fixture.db.Model(&orderdomain.Order{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("order leaked %d %v", count, err)
	}
}

// TestPublicSaleMissingSchemaFailsClosed 验证启用保护后不吞未迁移表错误。
func TestPublicSaleMissingSchemaFailsClosed(t *testing.T) {
	fixture := setupWholesaleOrderFixture(t, "missing_membership_schema", nil, nil, nil)
	fixture.svc.enforcePublicSale = true
	_, err := fixture.svc.PreviewOrder(CreateOrderInput{UserID: 1, Items: []CreateOrderItem{{ProductID: fixture.product.ID, Quantity: 1}}})
	if err == nil {
		t.Fatal("missing membership schema must fail closed")
	}
}
