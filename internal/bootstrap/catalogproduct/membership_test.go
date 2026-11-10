package catalogproductbootstrap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/catalogproduct"
	card "github.com/XManCodeing/XMall/internal/modules/cardsecret/domain"
	cards "github.com/XManCodeing/XMall/internal/modules/cardsecret/infrastructure/gormstore"
	cart "github.com/XManCodeing/XMall/internal/modules/cart/domain"
	carts "github.com/XManCodeing/XMall/internal/modules/cart/infrastructure/gormstore"
	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	categories "github.com/XManCodeing/XMall/internal/modules/catalog/category/infrastructure/gormstore"
	write "github.com/XManCodeing/XMall/internal/modules/catalog/product/application/write"
	pc "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	products "github.com/XManCodeing/XMall/internal/modules/catalog/product/store/gormstore"
	producthttp "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/http"
	member "github.com/XManCodeing/XMall/internal/modules/memberlevel/domain"
	members "github.com/XManCodeing/XMall/internal/modules/memberlevel/infrastructure/gormstore"
	order "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orders "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	ms "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMembershipShareHTTP verifies list and detail use the identical share projection,
// invalid products do not fail the list, and real database errors use the existing path.
// t owns a production-wired handler backed by an isolated SQLite database.
func TestMembershipShareHTTP(t *testing.T) {
	services, db := adminSetup(t)
	if err := db.AutoMigrate(&rd.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rd.Instance{ID: 9, TelegramID: 900, Username: "RealMallBot", Enabled: true, Mode: "internal"}).Error; err != nil {
		t.Fatal(err)
	}
	for id := uint(1); id <= 3; id++ {
		for _, row := range []interface{}{
			&p.Product{ID: id, Slug: "share-" + strconv.Itoa(int(id)), IsActive: true, TitleJSON: jsonmap.JSON{"en-US": "Membership"}},
			&d.ProductConfig{ProductID: id, Enabled: true, Version: 1, Quantity: 1},
			&d.ProductTarget{ProductID: id, TargetID: id},
		} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	store := ms.New(db)
	handler := producthttp.NewAdminProductHandler(services.Read, services.Write, services.Admin, nil, nil, nil).
		WithMembership(store, products.NewProductStore(db)).WithMembershipShare(store, store)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	producthttp.RegisterMembershipRoutes(router.Group("/admin"), handler)
	base := "/admin/telegram-bot/membership/products"
	request := func(path string) map[string]json.RawMessage {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if strings.Contains(recorder.Body.String(), "share-http-test-secret") || strings.Contains(recorder.Body.String(), "product_") {
			t.Fatal("response leaked secret or legacy payload")
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	for id := 1; id <= 2; id++ {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("POST", base+"/"+strconv.Itoa(id)+"/share", nil))
		if recorder.Code != 200 {
			t.Fatal(recorder.Body.String())
		}
	}
	list := request(base)
	var rows []struct {
		ID     uint   `json:"id"`
		URL    string `json:"share_url"`
		Reason string `json:"share_unavailable_reason"`
	}
	if err := json.Unmarshal(list["data"], &rows); err != nil {
		t.Fatalf("list: %s %v", list["data"], err)
	}
	if len(rows) != 3 {
		t.Fatalf("list count %d", len(rows))
	}
	for _, row := range rows {
		detail := request(base + "/" + strconv.Itoa(int(row.ID)))
		var item map[string]interface{}
		if err := json.Unmarshal(detail["data"], &item); err != nil {
			t.Fatal(err)
		}
		if row.ID != 3 {
			membershipView := item["membership"].(map[string]interface{})
			names := membershipView["target_names"].(map[string]interface{})
			if names[strconv.Itoa(int(row.ID))] != "member" {
				t.Fatalf("missing group name: %+v", names)
			}
		}
		if item["share_unavailable_reason"] != row.Reason {
			t.Fatalf("list/detail reasons differ: %+v %+v", row, item)
		}
		if row.ID == 3 {
			if row.URL != "" || item["share_url"] != "" || row.Reason != "missing_binding" {
				t.Fatalf("invalid target: %+v", row)
			}
		} else {
			for _, value := range []string{row.URL, item["share_url"].(string)} {
				link, err := url.Parse(value)
				if err != nil || link.Scheme != "https" || link.Host != "t.me" || link.Path != "/RealMallBot" || row.Reason != "" {
					t.Fatalf("invalid link %q: %v", value, err)
				}
				id, err := store.ResolveProductShare(context.Background(), link.Query().Get("start"))
				if err != nil || id != row.ID {
					t.Fatalf("invalid payload: product=%d err=%v", id, err)
				}
			}
		}
	}
	if err := db.Migrator().DropTable(&rd.Instance{}); err != nil {
		t.Fatal(err)
	}
	failed := request(base)
	var code int
	if err := json.Unmarshal(failed["status_code"], &code); err != nil {
		t.Fatal(err)
	}
	if code != 500 {
		t.Fatalf("database failure hidden: %+v", failed)
	}
}

// TestMembershipSalesCAS exercises activation, generic-write isolation and active edits using production UoW.
// t owns an isolated database; no service or remote payment is started.
func TestMembershipSalesCAS(t *testing.T) {
	service, db, product := operationSetup(t)
	ctx := context.Background()
	id := strconv.Itoa(int(product.ID))
	if err := db.Create(&category.Category{ID: 1, Slug: "members", NameJSON: jsonmap.JSON{"en-US": "Members"}, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&p.Product{}).Where("id = ?", product.ID).Update("category_id", 1).Error; err != nil {
		t.Fatal(err)
	}
	repo := products.NewProductStore(db)
	if err := repo.CheckPublicSale([]uint{product.ID}); !errors.Is(err, pc.ErrMembershipSaleClosed) {
		t.Fatalf("disabled sale: %v", err)
	}
	active, err := service.Admin.UpdateMembershipStatus(ctx, id, true, 1)
	if err != nil || !active.IsActive {
		t.Fatalf("activate: %+v %v", active, err)
	}
	config, err := ms.New(db).Config(ctx, product.ID)
	if err != nil || !config.Enabled || config.Version != 2 {
		t.Fatalf("non-atomic activation: %+v %v", config, err)
	}
	if err := repo.CheckPublicSale([]uint{product.ID}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckOrdinaryWrite([]uint{product.ID}); !errors.Is(err, pc.ErrMembershipSaleClosed) {
		t.Fatalf("generic write bypass: %v", err)
	}
	if err := service.Admin.Delete(id); !errors.Is(err, pc.ErrMembershipSaleClosed) {
		t.Fatalf("generic delete bypass: %v", err)
	}
	input := adminInput()
	input.CategoryID, input.Membership.ExpectedVersion, input.Membership.Enabled = 1, 2, true
	updated, err := service.Write.Update(id, input)
	if err != nil || !updated.IsActive {
		t.Fatalf("active edit: %+v %v", updated, err)
	}
	input.Membership.ExpectedVersion, input.Membership.Enabled = 3, false
	if _, err := service.Write.Update(id, input); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("legacy false silently changed status: %v", err)
	}
	if err := db.Model(&gd.Target{}).Where("id > 0").Update("obs_observed_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Admin.UpdateMembershipStatus(ctx, id, false, 3); err != nil {
		t.Fatalf("stale observation blocked deactivation: %v", err)
	}
	if _, err := service.Admin.UpdateMembershipStatus(ctx, id, true, 4); !errors.Is(err, c.ErrCapability) {
		t.Fatalf("stale observation activation: %v", err)
	}
	config, err = ms.New(db).Config(ctx, product.ID)
	if err != nil || config.Enabled || config.Version != 4 {
		t.Fatalf("failed activation changed CAS: %+v %v", config, err)
	}
}

// TestMembershipActivationRequiresFullSKUCoverage rejects a partial stored definition.
// t uses the production status transaction; rejection must leave both state and version unchanged.
func TestMembershipActivationRequiresFullSKUCoverage(t *testing.T) {
	service, db, product := operationSetup(t)
	if err := db.Where("product_id = ?", product.ID).Delete(&d.SKUTerm{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Admin.UpdateMembershipStatus(context.Background(), strconv.Itoa(int(product.ID)), true, 1); !errors.Is(err, c.ErrInvalid) {
		t.Fatalf("partial definition activated: %v", err)
	}
	config, err := ms.New(db).Config(context.Background(), product.ID)
	if err != nil || config.Enabled || config.Version != 1 {
		t.Fatalf("partial activation leaked: %+v %v", config, err)
	}
}

// TestAdminProductListsSeparateMembership 验证两个真实 HTTP 列表互斥，搜索、状态及分页总数同样隔离。
// 普通 member 购买身份不是会员群类型；默认仓储读取仍保留全部商品。
func TestAdminProductListsSeparateMembership(t *testing.T) {
	services, db := adminSetup(t)
	for id := uint(1); id <= 4; id++ {
		row := p.Product{ID: id, Slug: fmt.Sprintf("scope-%d", id), TitleJSON: jsonmap.JSON{"zh-CN": "范围测试"}, PurchaseType: "member", FulfillmentType: "manual", IsActive: id%2 == 0}
		if err := db.Omit("Category", "SKUs").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint{3, 4} {
		if err := db.Create(&d.ProductConfig{ProductID: id, Version: 1, Quantity: 1, Enabled: false}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := products.NewProductStore(db)
	handler := producthttp.NewAdminProductHandler(services.Read, services.Write, services.Admin, nil, nil, nil).WithMembership(ms.New(db), repo)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	producthttp.RegisterAdminRoutes(router.Group("/admin"), handler)
	producthttp.RegisterMembershipRoutes(router.Group("/admin"), handler)
	for _, test := range []struct {
		path       string
		total      int64
		length     int
		membership bool
	}{
		{"/products?page_size=1", 2, 1, false},
		{"/products?page_size=1&page=2", 2, 1, false},
		{"/products?search=scope-3", 0, 0, false},
		{"/products?is_active=1", 1, 1, false},
		{"/products?is_active=0", 1, 1, false},
		{"/products?only_membership=true", 2, 2, false},
		{"/telegram-bot/membership/products", 2, 2, true},
		{"/telegram-bot/membership/products?search=scope-1", 0, 0, true},
		{"/telegram-bot/membership/products?is_active=0", 1, 1, true},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/admin"+test.path, nil))
			var envelope struct {
				StatusCode int         `json:"status_code"`
				Data       []p.Product `json:"data"`
				Pagination struct {
					Total int64 `json:"total"`
				} `json:"pagination"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || envelope.StatusCode != 0 || envelope.Pagination.Total != test.total || len(envelope.Data) != test.length {
				t.Fatalf("unexpected list: %s", response.Body.String())
			}
			for _, row := range envelope.Data {
				if (row.ID >= 3) != test.membership {
					t.Fatalf("cross-list product: %d", row.ID)
				}
			}
		})
	}
	rows, total, err := repo.List(pc.ListFilter{Page: 1, PageSize: 20})
	if err != nil || total != 4 || len(rows) != 4 {
		t.Fatalf("unscoped callers changed: total=%d error=%v", total, err)
	}
}

// adminSetup 构造生产商品 UoW 和单连接 SQLite；所有能力仅来自测试观测。
func adminSetup(t *testing.T) (bootstrap.Services, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{TranslateError: true})
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
	if err = db.AutoMigrate(&category.Category{}, &p.Product{}, &p.ProductSKU{}, &gd.Target{}, &d.ProductShareCode{}, &d.ProductConfig{}, &d.ProductTarget{}, &d.SKUTerm{}, &d.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for id := uint(1); id <= 2; id++ {
		target := gd.Target{ID: id, Name: "member", Purpose: "membership", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 9, TelegramChatID: -1000 - int64(id), OccupancySlot: fmt.Sprintf("chat:%d", id), Observation: gd.Observation{RouteVersion: 1, ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true, State: "ready", ObservedAt: &now}}
		if err = db.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
	}
	return bootstrap.New(bootstrap.Dependencies{EnforcePublicSale: true, Products: products.NewProductStore(db), SKUs: products.NewSKUStore(db), Categories: categories.NewCategoryStore(db)}), db
}

// operationSetup 使用临时数据库及生产 UoW 创建无库存会员，返回服务/连接/商品。
func operationSetup(t *testing.T) (bootstrap.Services, *gorm.DB, *p.Product) {
	t.Helper()
	initial, db := adminSetup(t)
	if err := db.AutoMigrate(&card.Secret{}, &card.Batch{}, &cart.Item{}, &member.MemberLevelPrice{}, &order.OrderItem{}, &d.LedgerSource{}, &d.LedgerProjection{}); err != nil {
		t.Fatal(err)
	}
	saved, err := initial.Write.Create(adminInput())
	if err != nil {
		t.Fatal(err)
	}
	service := bootstrap.New(bootstrap.Dependencies{EnforcePublicSale: true, Products: products.NewProductStore(db), SKUs: products.NewSKUStore(db), CardSecrets: cards.New(db), CardSecretBatches: cards.NewBatch(db), MemberLevelPrices: members.NewPriceStore(db), Carts: carts.New(db), Orders: orders.New(db, "test-guest-credential-secret-with-32-bytes")})
	return service, db, saved
}

// TestMembershipDeactivatePreservesHistory 验证过期权限不阻止停销且所有历史权益原样保留。
func TestMembershipDeactivatePreservesHistory(t *testing.T) {
	service, db, product := operationSetup(t)
	id := strconv.Itoa(int(product.ID))
	snapshot := d.OrderItemSnapshot{OrderItemID: 901, ProductID: product.ID, SKUID: product.SKUs[0].ID, Kind: "permanent", Quantity: 1, Targets: []d.SnapshotTarget{}}
	source := d.LedgerSource{OrderItemID: 901, TargetID: 1, Operation: "grant", Kind: "permanent"}
	for _, row := range []any{&snapshot, &source} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&gd.Target{}).Where("id > 0").Update("obs_observed_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&p.Product{}).Where("id = ?", product.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	updated, err := service.Admin.UpdateMembershipStatus(context.Background(), id, false, 1)
	if err != nil || updated.IsActive {
		t.Fatalf("deactivate %+v %v", updated, err)
	}
	config, err := ms.New(db).Config(context.Background(), product.ID)
	if err != nil || config.Version != 2 || config.Enabled {
		t.Fatalf("config %+v %v", config, err)
	}
	var actual d.LedgerSource
	if err := db.First(&actual, source.ID).Error; err != nil || actual.Kind != "permanent" {
		t.Fatalf("source mutated %+v %v", actual, err)
	}
	if err := service.Admin.DeleteMembership(context.Background(), id, 2); !errors.Is(err, pc.ErrProductHasOrderRecord) {
		t.Fatalf("history bypass: %v", err)
	}
	if _, err := service.Admin.UpdateMembershipStatus(context.Background(), id, true, 2); !errors.Is(err, c.ErrInvalid) {
		t.Fatalf("sale bypass: %v", err)
	}
	if _, err := service.Admin.UpdateMembershipStatus(context.Background(), id, false, 1); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
}

// TestMembershipDeleteCascade 验证成功清理会员扩展与 SKU，不遗留配置或期限。
func TestMembershipDeleteCascade(t *testing.T) {
	service, db, product := operationSetup(t)
	id := strconv.Itoa(int(product.ID))
	if err := service.Admin.DeleteMembership(context.Background(), id, 2); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale delete %v", err)
	}
	if err := service.Admin.DeleteMembership(context.Background(), id, 1); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&d.ProductConfig{}, &d.ProductTarget{}, &d.SKUTerm{}} {
		var count int64
		if err := db.Model(model).Where("product_id = ?", product.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("orphan %T %d %v", model, count, err)
		}
	}
	actual, err := products.NewProductStore(db).GetByID(id)
	if err != nil || actual != nil {
		t.Fatalf("product remains %+v %v", actual, err)
	}
}

// TestMembershipDeleteProtection 覆盖实库存、SKU库存、订单与独立历史快照，并验证回滚版本。
func TestMembershipDeleteProtection(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(*gorm.DB, *p.Product) error
		expected error
	}{
		{"manual stock", func(db *gorm.DB, pd *p.Product) error { return db.Model(pd).Update("manual_stock_total", 2).Error }, pc.ErrProductHasStock},
		{"sku locked", func(db *gorm.DB, pd *p.Product) error {
			return db.Model(&p.ProductSKU{}).Where("product_id = ?", pd.ID).Update("manual_stock_locked", 1).Error
		}, pc.ErrProductHasStock},
		{"card stock", func(db *gorm.DB, pd *p.Product) error {
			return db.Create(&card.Secret{ProductID: pd.ID, SKUID: pd.SKUs[0].ID, Status: card.StatusAvailable}).Error
		}, pc.ErrProductHasStock},
		{"order", func(db *gorm.DB, pd *p.Product) error {
			return db.Create(&order.OrderItem{TitleJSON: jsonmap.JSON{"en-US": "test"}, OrderID: 700, ProductID: pd.ID, SKUID: pd.SKUs[0].ID}).Error
		}, pc.ErrProductHasOrderRecord},
		{"historical snapshot", func(db *gorm.DB, pd *p.Product) error {
			return db.Create(&d.OrderItemSnapshot{OrderItemID: 701, ProductID: pd.ID, Targets: []d.SnapshotTarget{}}).Error
		}, pc.ErrProductHasOrderRecord},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, db, product := operationSetup(t)
			if err := test.seed(db, product); err != nil {
				t.Fatal(err)
			}
			if err := service.Admin.DeleteMembership(context.Background(), strconv.Itoa(int(product.ID)), 1); !errors.Is(err, test.expected) {
				t.Fatalf("protection %v", err)
			}
			config, err := ms.New(db).Config(context.Background(), product.ID)
			if err != nil || config.Version != 1 {
				t.Fatalf("partial CAS %+v %v", config, err)
			}
		})
	}
}

// TestMembershipOperationCASConcurrent 验证同版本并发停销只能提交一次，不发生丢失更新。
func TestMembershipOperationCASConcurrent(t *testing.T) {
	service, _, product := operationSetup(t)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Admin.UpdateMembershipStatus(context.Background(), strconv.Itoa(int(product.ID)), false, 1)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, c.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS successes=%d conflicts=%d", successes, conflicts)
	}
}

// TestMembershipDeleteRollback 验证原商品删除失败时会员根/期限/群关联及 SKU 全部回滚。
func TestMembershipDeleteRollback(t *testing.T) {
	service, db, product := operationSetup(t)
	failure := errors.New("injected product delete failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:fail-product-delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "products" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Admin.DeleteMembership(context.Background(), strconv.Itoa(int(product.ID)), 1); !errors.Is(err, failure) {
		t.Fatalf("expected rollback %v", err)
	}
	config, err := ms.New(db).Config(context.Background(), product.ID)
	if err != nil || config.Version != 1 || len(config.Terms) != 1 || len(config.Targets) != 2 {
		t.Fatalf("partial config %+v %v", config, err)
	}
	actual, err := products.NewProductStore(db).GetAdminByID(strconv.Itoa(int(product.ID)))
	if err != nil || actual == nil || len(actual.SKUs) != 1 {
		t.Fatalf("partial cascade %+v %v", actual, err)
	}
}

// TestMembershipDeleteVersusPatchCAS 验证相同版本删除与下架竞争时只能提交一个操作。
func TestMembershipDeleteVersusPatchCAS(t *testing.T) {
	service, _, product := operationSetup(t)
	id := strconv.Itoa(int(product.ID))
	results := make(chan error, 2)
	go func() { results <- service.Admin.DeleteMembership(context.Background(), id, 1) }()
	go func() {
		_, err := service.Admin.UpdateMembershipStatus(context.Background(), id, false, 1)
		results <- err
	}()
	successes := 0
	for index := 0; index < 2; index++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, c.ErrConflict) && !errors.Is(err, c.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("committed operations=%d", successes)
	}
}

// TestMembershipOperationsHTTP 验证布尔/版本边界、普通商品拒绝和稳定 HTTP 运营合同。
func TestMembershipOperationsHTTP(t *testing.T) {
	service, db, product := operationSetup(t)
	ordinary := p.Product{Slug: "ordinary", TitleJSON: jsonmap.JSON{"en-US": "ordinary"}}
	if err := db.Create(&ordinary).Error; err != nil {
		t.Fatal(err)
	}
	handler := producthttp.NewAdminProductHandler(service.Read, service.Write, service.Admin, nil, nil, nil).WithMembership(ms.New(db), products.NewProductStore(db))
	router := gin.New()
	producthttp.RegisterMembershipRoutes(router.Group("/admin"), handler)
	path := "/admin/telegram-bot/membership/products/" + strconv.Itoa(int(product.ID))
	tests := []struct {
		method, path, body string
		http, code         int
	}{
		{"PATCH", path, `{"is_active":true,"expected_version":1}`, 200, 400},
		{"PATCH", path, `{"is_active":false}`, 200, 400},
		{"PATCH", path, `{"expected_version":1}`, 200, 400},
		{"PATCH", path, `{"is_active":false,"expected_version":0}`, 200, 400},
		{"PATCH", path, `{"is_active":false,"expected_version":1}`, 200, 0},
		{"PATCH", path, `{"is_active":false,"expected_version":1}`, 409, 409},
		{"DELETE", path, "", 200, 400},
		{"DELETE", path + "?expected_version=1", "", 409, 409},
		{"PATCH", "/admin/telegram-bot/membership/products/" + strconv.Itoa(int(ordinary.ID)), `{"is_active":false,"expected_version":1}`, 200, 404},
		{"DELETE", "/admin/telegram-bot/membership/products/" + strconv.Itoa(int(ordinary.ID)) + "?expected_version=1", "", 200, 404},
		{"DELETE", path + "?expected_version=2", "", 200, 0},
	}
	for _, test := range tests {
		req := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		var payload struct {
			Code int `json:"status_code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != test.http || payload.Code != test.code {
			t.Fatalf("%s %s body=%s: %d %s", test.method, test.path, test.body, recorder.Code, recorder.Body.String())
		}
	}
}

// adminInput 返回 DEFAULT SKU 输入和固定一份的两个月配置。
func adminInput() write.CreateProductInput {
	return write.CreateProductInput{Slug: "new-membership", TitleJSON: map[string]interface{}{"zh-CN": "会员"}, PriceAmount: decimal.NewFromInt(10), PurchaseType: "member", FulfillmentType: "manual", StockDisplayMode: "status", Membership: &c.AdminConfigInput{Quantity: 1, TargetIDs: []uint{1, 2}, Terms: []c.AdminTerm{{SKUCode: "DEFAULT", Kind: "months", Months: 2}}}}
}

// TestAdminProductAtomicDefaultCAS 验证 DEFAULT、2/5/18 月、永久和陈旧 CAS 全回滚。
func TestAdminProductAtomicDefaultCAS(t *testing.T) {
	services, db := adminSetup(t)
	writer := services.Write
	input := adminInput()
	saved, err := writer.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if saved.IsActive || saved.MinPurchaseQuantity != 1 || saved.MaxPurchaseQuantity != 1 || len(saved.SKUs) != 1 {
		t.Fatalf("unsafe product %+v", saved)
	}
	id := strconv.Itoa(int(saved.ID))
	for index, months := range []int64{5, 18, 0} {
		input.Membership.ExpectedVersion = uint64(index + 1)
		input.Membership.Terms[0].Months = months
		if months == 0 {
			input.Membership.Terms[0].Kind = "permanent"
		}
		if _, err = writer.Update(id, input); err != nil {
			t.Fatal(err)
		}
	}
	input.TitleJSON = map[string]interface{}{"zh-CN": "must rollback"}
	input.PriceAmount = decimal.NewFromInt(99)
	if _, err = writer.Update(id, input); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale CAS %v", err)
	}
	actual, err := products.NewProductStore(db).GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if actual.PriceAmount.String() != "10.00" || actual.SKUs[0].PriceAmount.String() != "10.00" || actual.TitleJSON["zh-CN"] != "会员" {
		t.Fatalf("partial stale write %+v", actual)
	}
	input.Membership = nil
	if _, err = writer.Update(id, input); !errors.Is(err, pc.ErrMembershipSaleClosed) {
		t.Fatalf("ordinary edit bypass %v", err)
	}
	if err = products.NewProductStore(db).QuickUpdatePublicSale(id, map[string]interface{}{"is_active": true}); !errors.Is(err, pc.ErrMembershipSaleClosed) {
		t.Fatalf("activation bypass %v", err)
	}
}

// TestAdminProductFailuresRollback 验证配置/期限/跨 Bot/未知权限失败时商品和 SKU 全回滚。
func TestAdminProductFailuresRollback(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*write.CreateProductInput, *gorm.DB) error
	}{
		{"invalid term", func(in *write.CreateProductInput, _ *gorm.DB) error { in.Membership.Terms[0].Months = 0; return nil }},
		{"missing sku", func(in *write.CreateProductInput, _ *gorm.DB) error {
			in.Membership.Terms[0].SKUCode = "unknown"
			return nil
		}},
		{"enabled", func(in *write.CreateProductInput, _ *gorm.DB) error { in.Membership.Enabled = true; return nil }},
		{"cross bot", func(_ *write.CreateProductInput, db *gorm.DB) error {
			return db.Model(&gd.Target{}).Where("id = 2").Update("bot_instance_id", 77).Error
		}},
		{"no permission", func(_ *write.CreateProductInput, db *gorm.DB) error {
			return db.Model(&gd.Target{}).Where("id = 2").Update("obs_can_invite_users", false).Error
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			services, db := adminSetup(t)
			in := adminInput()
			if err := test.mutate(&in, db); err != nil {
				t.Fatal(err)
			}
			if _, err := services.Write.Create(in); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			for _, model := range []interface{}{&p.Product{}, &p.ProductSKU{}, &d.ProductConfig{}} {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("partial write %T %d %v", model, count, err)
				}
			}
		})
	}
}

// TestAdminNewSKUCodeBinding 验证新多规格编码映射和历史快照不变。
func TestAdminNewSKUCodeBinding(t *testing.T) {
	services, db := adminSetup(t)
	in := adminInput()
	in.SKUs = []write.ProductSKUInput{{SKUCode: "M2", PriceAmount: decimal.NewFromInt(2)}, {SKUCode: "M18", PriceAmount: decimal.NewFromInt(18)}}
	in.Membership.Terms = []c.AdminTerm{{SKUCode: "M2", Kind: "months", Months: 2}, {SKUCode: "M18", Kind: "months", Months: 18}}
	product, err := services.Write.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&d.OrderItemSnapshot{OrderItemID: 901, ProductID: product.ID, SKUID: product.SKUs[0].ID, Kind: "months", Months: 2, Quantity: 1, Targets: []d.SnapshotTarget{}}).Error; err != nil {
		t.Fatal(err)
	}
	in.Membership.ExpectedVersion = 1
	in.Membership.Terms[0].Months = 5
	if _, err = services.Write.Update(strconv.Itoa(int(product.ID)), in); err != nil {
		t.Fatal(err)
	}
	var snapshot d.OrderItemSnapshot
	if err = db.First(&snapshot, "order_item_id = 901").Error; err != nil || snapshot.Months != 2 {
		t.Fatalf("snapshot changed %+v %v", snapshot, err)
	}
}

// TestMembershipProductHTTPContract 验证真实 HTTP 一次保存、CAS 状态码及普通端点拒绝扩展注入。
func TestMembershipProductHTTPContract(t *testing.T) {
	services, db := adminSetup(t)
	if err := db.Create(&category.Category{ID: 1, Slug: "members", NameJSON: jsonmap.JSON{"en-US": "Members"}, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	repo := products.NewProductStore(db)
	handler := producthttp.NewAdminProductHandler(services.Read, services.Write, services.Admin, nil, nil, nil).WithMembership(ms.New(db), repo)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/admin")
	producthttp.RegisterAdminRoutes(group, handler)
	producthttp.RegisterMembershipRoutes(group, handler)
	body := map[string]interface{}{"category_id": 1, "slug": "http-member", "title": map[string]string{"zh-CN": "会员"}, "price_amount": 10, "purchase_type": "member", "fulfillment_type": "manual", "stock_display_mode": "status", "membership": adminInput().Membership}
	request := func(method, path string) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
	result := request("POST", "/admin/telegram-bot/membership/products")
	if result.Code != 200 {
		t.Fatalf("create %d %s", result.Code, result.Body.String())
	}
	var payload struct {
		Data struct {
			ID         uint `json:"id"`
			Membership struct {
				Version uint64        `json:"version"`
				Terms   []c.AdminTerm `json:"terms"`
			} `json:"membership"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID == 0 || payload.Data.Membership.Version != 1 || len(payload.Data.Membership.Terms) != 1 || payload.Data.Membership.Terms[0].SKUCode != "DEFAULT" {
		t.Fatalf("contract %s", result.Body.String())
	}
	path := "/admin/telegram-bot/membership/products/" + strconv.Itoa(int(payload.Data.ID))
	if result = request("PUT", path); result.Code != 409 {
		t.Fatalf("CAS %d %s", result.Code, result.Body.String())
	}
	if result = request("POST", "/admin/products"); !bytes.Contains(result.Body.Bytes(), []byte(`"status_code":400`)) {
		t.Fatalf("ordinary injection %d %s", result.Code, result.Body.String())
	}
	if result = request("GET", "/admin/telegram-bot/membership/products/999"); !bytes.Contains(result.Body.Bytes(), []byte(`"status_code":404`)) {
		t.Fatalf("ordinary detail %d %s", result.Code, result.Body.String())
	}
}
