package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"

	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"

	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"

	categoryapp "github.com/dujiao-next/internal/modules/catalog/category/application"
	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"

	"github.com/dujiao-next/internal/constants"
	categorygormstore "github.com/dujiao-next/internal/modules/catalog/category/infrastructure/gormstore"
	mappingapp "github.com/dujiao-next/internal/modules/catalog/mapping/application"
	mappingcontract "github.com/dujiao-next/internal/modules/catalog/mapping/contract"
	mappinggormstore "github.com/dujiao-next/internal/modules/catalog/mapping/infrastructure/gormstore"
	mappinghttp "github.com/dujiao-next/internal/modules/catalog/mapping/transport/http"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/upstream"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type adminNoopLocalMediaRecorder struct{}

func (adminNoopLocalMediaRecorder) RecordLocalFile(context.Context, string, string) {}

type mappingConnectionProvider struct {
	db      *gorm.DB
	adapter upstream.Adapter
}

func (p *mappingConnectionProvider) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := p.db.First(&conn, id).Error; err != nil {
		return nil, err
	}
	return &conn, nil
}

func (p *mappingConnectionProvider) GetAdapter(conn *siteconnectiondomain.Connection) (upstream.Adapter, error) {
	return p.adapter, nil
}

type mappingImportUoW struct {
	products    *productgormstore.ProductStore
	skus        *productgormstore.SKUStore
	mappings    *mappinggormstore.MappingStore
	skuMappings *mappinggormstore.SKUMappingStore
}

func (unit *mappingImportUoW) WithinTransaction(fn func(mappingcontract.ImportRepositories) error) error {
	if fn == nil {
		return nil
	}
	return unit.products.Transaction(func(tx *gorm.DB) error {
		return fn(mappingcontract.ImportRepositories{
			Products:    unit.products.BindTx(tx),
			SKUs:        unit.skus.BindTx(tx),
			Mappings:    unit.mappings.WithTx(tx),
			SKUMappings: unit.skuMappings.WithTx(tx),
		})
	})
}

func setupAdminHandlerTest(t *testing.T, upstreamHandler http.HandlerFunc, overrides ...upstream.Adapter) (*mappinghttp.AdminHandler, *gorm.DB, uint, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_product_mapping_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&categorydomain.Category{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&siteconnectiondomain.Connection{},
		&mappingdomain.Mapping{},
		&mappingdomain.SKUMapping{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	server := httptest.NewServer(upstreamHandler)
	categoryRepo := categorygormstore.NewCategoryStore(db)
	categoryService := categoryapp.NewService(categoryRepo)

	conn := siteconnectiondomain.Connection{
		Name:      "upstream",
		BaseURL:   server.URL,
		ApiKey:    "test-key",
		ApiSecret: "test-secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
		Status:    constants.ConnectionStatusActive,
	}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatalf("create connection failed: %v", err)
	}

	adapter, err := upstream.NewAdapter(&conn, t.TempDir())
	if err != nil {
		t.Fatalf("create upstream adapter failed: %v", err)
	}

	if len(overrides) > 0 {
		adapter = overrides[0]
	}
	productStore := productgormstore.NewProductStore(db)
	skuStore := productgormstore.NewSKUStore(db)
	mappingStore := mappinggormstore.NewMappingStore(db)
	skuMappingStore := mappinggormstore.NewSKUMappingStore(db)

	mappingService, err := mappingapp.NewService(mappingapp.Options{
		Mappings:    mappingStore,
		SKUMappings: skuMappingStore,
		Products:    productStore,
		SKUs:        skuStore,
		Categories:  categoryRepo,
		Connections: &mappingConnectionProvider{db: db, adapter: adapter},
		Media:       adminNoopLocalMediaRecorder{},
		Transactions: &mappingImportUoW{
			products:    productStore,
			skus:        skuStore,
			mappings:    mappingStore,
			skuMappings: skuMappingStore,
		},
	})
	if err != nil {
		t.Fatalf("create product mapping service: %v", err)
	}
	mappingService.SetCategoryCreator(categoryService)

	h := mappinghttp.NewAdminHandler(mappingService)
	return h, db, conn.ID, server.Close
}

type opaqueCatalogAdapter struct {
	upstream.Adapter
	product upstream.UpstreamProduct
}

func (a *opaqueCatalogAdapter) GetProductExternal(_ context.Context, id string) (*upstream.UpstreamProduct, error) {
	if id != a.product.ExternalID {
		return nil, fmt.Errorf("unexpected external ID %q", id)
	}
	return &a.product, nil
}

func TestOpaqueImportAndRepeatedSyncKeepSKUMappingsStable(t *testing.T) {
	for _, productLevel := range []bool{false, true} {
		t.Run(fmt.Sprint(productLevel), func(t *testing.T) {
			adapter := &opaqueCatalogAdapter{product: upstream.UpstreamProduct{ExternalID: "001/商品", Title: jsonmap.JSON{"en-US": "Opaque"}, PriceAmount: "10", IsActive: true, SKUs: []upstream.UpstreamSKU{
				{ExternalID: "001", PriceAmount: "10", IsActive: true, StockQuantity: 3},
				{ExternalID: "1", PriceAmount: "20", IsActive: true, StockQuantity: 4},
			}}}
			if productLevel {
				adapter.product.SKUs = []upstream.UpstreamSKU{{ProductLevel: true, PriceAmount: "10", IsActive: true, StockQuantity: 3}}
			}
			h, db, connectionID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream HTTP") }, adapter)
			defer cleanup()
			category := &categorydomain.Category{Slug: "opaque-category", NameJSON: jsonmap.JSON{"en-US": "Opaque"}, IsActive: true}
			if err := db.Create(category).Error; err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_external_id":"001/商品","category_id":%d}`, connectionID, category.ID)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			h.ImportUpstreamProduct(c)
			var mapping mappingdomain.Mapping
			if err := db.First(&mapping).Error; err != nil {
				t.Fatalf("import failed: %s %v", w.Body.String(), err)
			}
			if mapping.UpstreamProductID != 0 || mapping.UpstreamProductExternalID != "001/商品" {
				t.Fatalf("reference lost: %+v", mapping)
			}
			var original []productdomain.ProductSKU
			db.Order("id").Find(&original)
			for i := 0; i < 2; i++ {
				w = httptest.NewRecorder()
				c, _ = gin.CreateTestContext(w)
				c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(mapping.ID)}}
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				h.SyncProductMapping(c)
				if !strings.Contains(w.Body.String(), `"synced":true`) {
					t.Fatalf("sync failed: %s", w.Body.String())
				}
			}
			var skus []productdomain.ProductSKU
			db.Order("id").Find(&skus)
			var mappings []mappingdomain.SKUMapping
			db.Order("id").Find(&mappings)
			if len(skus) != len(adapter.product.SKUs) || len(mappings) != len(skus) {
				t.Fatalf("SKU mappings not stable: %d %d", len(skus), len(mappings))
			}
			for i := range skus {
				if skus[i].ID != original[i].ID || skus[i].SKUCode != original[i].SKUCode || skus[i].SKUCode == "" {
					t.Fatalf("local SKU identity changed: %+v", skus)
				}
				if mappings[i].UpstreamSKUExternalID != adapter.product.SKUs[i].ExternalID || mappings[i].ProductLevel != productLevel {
					t.Fatalf("opaque SKU mapping lost: %+v", mappings)
				}
			}
		})
	}
}

type paginationMappingService struct {
	mappinghttp.ProductMappingService
	opts upstream.ListProductsOpts
}

func (s *paginationMappingService) ImportUpstreamProductExternal(uint, string, uint, string, bool) (*mappingdomain.Mapping, error) {
	panic("unexpected")
}
func (s *paginationMappingService) BatchImportUpstreamProductsExternal(uint, []string, uint, bool) ([]mappingapp.BatchUpstreamProductImportOutcome, error) {
	panic("unexpected")
}
func (s *paginationMappingService) GetMappedUpstreamIDs(uint) ([]uint, error) { return []uint{1}, nil }
func (s *paginationMappingService) GetMappedUpstreamExternalIDs(uint) ([]string, error) {
	return []string{"001"}, nil
}
func (s *paginationMappingService) ListUpstreamProductsWithOptions(_ uint, opts upstream.ListProductsOpts) (*upstream.ProductListResult, error) {
	s.opts = opts
	more := true
	return &upstream.ProductListResult{Items: []upstream.UpstreamProduct{{ExternalID: "001"}}, HasMore: &more, NextCursor: "next opaque"}, nil
}
func TestListProductsExposesOpaquePaginationAndMappedIDs(t *testing.T) {
	svc := &paginationMappingService{}
	h := mappinghttp.NewAdminHandler(svc)
	for _, query := range []string{"?connection_id=1", "?connection_id=1&cursor=opaque%2Fnext&offset=12"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/"+query, nil)
		h.ListUpstreamProducts(c)
		var payload struct {
			Data struct {
				TotalKnown        bool     `json:"total_known"`
				HasMore           *bool    `json:"has_more"`
				NextCursor        string   `json:"next_cursor"`
				MappedExternalIDs []string `json:"mapped_external_ids"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Data.TotalKnown || payload.Data.HasMore == nil || !*payload.Data.HasMore || payload.Data.NextCursor != "next opaque" {
			t.Fatalf("pagination metadata lost: %s", w.Body.String())
		}
		if strings.Contains(query, "offset") {
			if svc.opts.Offset != 12 || svc.opts.Cursor != "opaque/next" {
				t.Fatalf("options lost: %+v", svc.opts)
			}
		} else if len(payload.Data.MappedExternalIDs) != 1 || payload.Data.MappedExternalIDs[0] != "001" {
			t.Fatalf("mapped external IDs lost: %s", w.Body.String())
		}
	}
}

type unreachableMappingService struct {
	mappinghttp.ProductMappingService
}

func TestImportRejectsMixedReferenceNamespaces(t *testing.T) {
	h := mappinghttp.NewAdminHandler(unreachableMappingService{})
	for _, tc := range []struct {
		body  string
		batch bool
	}{
		{`{"connection_id":1,"upstream_product_id":1,"upstream_product_external_id":"001"}`, false},
		{`{"connection_id":1,"upstream_product_id":0,"upstream_product_external_id":"001"}`, false},
		{`{"connection_id":1,"upstream_product_external_id":""}`, false},
		{`{"connection_id":1,"upstream_product_ids":[1],"upstream_product_external_ids":["001"]}`, true},
		{`{"connection_id":1,"upstream_product_ids":[],"upstream_product_external_ids":["001"]}`, true},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		if tc.batch {
			h.BatchImportUpstreamProducts(c)
		} else {
			h.ImportUpstreamProduct(c)
		}
		var payload struct {
			StatusCode int `json:"status_code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.StatusCode != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestBatchImportUpstreamProductsAutoCreatesCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					Description:     jsonmap.JSON{"zh-CN": "描述"},
					Content:         jsonmap.JSON{"zh-CN": "内容"},
					Images:          []string{},
					Tags:            []string{},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID == 0 {
		t.Fatalf("expected imported product to be assigned to auto-created category")
	}

	var category categorydomain.Category
	if err := db.First(&category, imported.CategoryID).Error; err != nil {
		t.Fatalf("load auto-created category failed: %v", err)
	}
	if category.Slug != "upstream-streaming" {
		t.Fatalf("expected auto-created category slug upstream-streaming, got %q", category.Slug)
	}
}

func TestBatchImportUpstreamProductsRestoresSoftDeletedAutoCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	deletedCategory := categorydomain.Category{
		Slug:     "upstream-streaming",
		NameJSON: jsonmap.JSON{"zh-CN": "已删除分类"},
		IsActive: true,
	}
	if err := db.Create(&deletedCategory).Error; err != nil {
		t.Fatalf("create soft-delete target category failed: %v", err)
	}
	if err := categorygormstore.NewCategoryStore(db).Delete(fmt.Sprintf("%d", deletedCategory.ID)); err != nil {
		t.Fatalf("soft delete category failed: %v", err)
	}

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID != deletedCategory.ID {
		t.Fatalf("expected imported product category %d, got %d", deletedCategory.ID, imported.CategoryID)
	}

	var restored categorydomain.Category
	if err := db.First(&restored, deletedCategory.ID).Error; err != nil {
		t.Fatalf("expected category to be restored, got %v", err)
	}
	if restored.NameJSON["zh-CN"] != "流媒体" {
		t.Fatalf("expected restored category name to be refreshed, got %+v", restored.NameJSON)
	}
}
