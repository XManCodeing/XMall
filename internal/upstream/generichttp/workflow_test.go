package generichttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	categorydomain "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	categorystore "github.com/XManCodeing/XMall/internal/modules/catalog/category/infrastructure/gormstore"
	mappingapp "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/application"
	mappingcontract "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/contract"
	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"
	mappingstore "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/infrastructure/gormstore"
	productdomain "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	productstore "github.com/XManCodeing/XMall/internal/modules/catalog/product/store/gormstore"
	fulfillmentdomain "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	orderdomain "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderstore "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	procurementapp "github.com/XManCodeing/XMall/internal/modules/procurement/application"
	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
	procurementdomain "github.com/XManCodeing/XMall/internal/modules/procurement/domain"
	procurementstore "github.com/XManCodeing/XMall/internal/modules/procurement/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/modules/procurement/infrastructure/mappingreader"
	"github.com/XManCodeing/XMall/internal/modules/procurement/infrastructure/orderreader"
	"github.com/XManCodeing/XMall/internal/modules/procurement/infrastructure/upstreamgateway"
	siteconnectiondomain "github.com/XManCodeing/XMall/internal/modules/siteconnection/domain"
	"github.com/XManCodeing/XMall/internal/shared/httpintegration"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	sharedmoney "github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/XManCodeing/XMall/internal/upstream"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Only the HTTP execution boundary is simulated. Response mapping, catalog
// import, gateway translation, procurement state transitions and delivery writes
// are production implementations. No server, global DB or SSRF override is used.
type workflowProvider struct {
	db *gorm.DB
	a  *adapter
}

func (p *workflowProvider) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var connection siteconnectiondomain.Connection
	err := p.db.First(&connection, id).Error
	return &connection, err
}

func (p *workflowProvider) GetAdapter(*siteconnectiondomain.Connection) (upstream.Adapter, error) {
	return p.a, nil
}

// Production generic connections require a frozen-connection provider. The test
// token contains no credentials and identifies the immutable adapter fixture;
// production encryption/configuration persistence is intentionally not mocked in.
func (p *workflowProvider) CaptureProcurementSnapshot(id uint) (string, error) {
	if _, err := p.GetByID(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("workflow-frozen:%d", id), nil
}

func (p *workflowProvider) OpenProcurementSnapshot(id uint, snapshot string) (*siteconnectiondomain.Connection, upstream.Adapter, error) {
	if snapshot != fmt.Sprintf("workflow-frozen:%d", id) {
		return nil, nil, errors.New("unexpected workflow snapshot")
	}
	connection, err := p.GetByID(id)
	return connection, p.a, err
}

type workflowMedia struct{}

func (workflowMedia) RecordLocalFile(context.Context, string, string) {}

type workflowImportTransaction struct{ db *gorm.DB }

func (u workflowImportTransaction) WithinTransaction(fn func(mappingcontract.ImportRepositories) error) error {
	return u.db.Transaction(func(tx *gorm.DB) error {
		return fn(mappingcontract.ImportRepositories{
			Products: productstore.NewProductStore(tx), SKUs: productstore.NewSKUStore(tx),
			Mappings: mappingstore.NewMappingStore(tx), SKUMappings: mappingstore.NewSKUMappingStore(tx),
		})
	})
}

type workflowFixture struct {
	db         *gorm.DB
	adapter    *adapter
	catalog    *mappingapp.Service
	procure    *procurementapp.Service
	connection uint
	category   uint
}

func newWorkflowFixture(t *testing.T, reply func(httpintegration.Endpoint, map[string]string) (string, error)) *workflowFixture {
	t.Helper()
	base, err := New("https://supplier.example", fixtureConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	a := base.(*adapter)
	a.execute = func(_ context.Context, _ string, endpoint httpintegration.Endpoint, variables map[string]string) (*httpintegration.Result, error) {
		body, err := reply(endpoint, variables)
		if err != nil {
			return nil, err
		}
		mapped, err := httpintegration.MapResponse(endpoint, []byte(body))
		return &httpintegration.Result{StatusCode: 200, Mapped: mapped}, err
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&categorydomain.Category{}, &productdomain.Product{}, &productdomain.ProductSKU{},
		&mappingdomain.Mapping{}, &mappingdomain.SKUMapping{}, &siteconnectiondomain.Connection{},
		&orderdomain.Order{}, &orderdomain.OrderItem{}, &orderdomain.OrderRefundRecord{},
		&fulfillmentdomain.Fulfillment{}, &procurementdomain.Order{},
	); err != nil {
		t.Fatal(err)
	}
	connection := siteconnectiondomain.Connection{
		Name: "workflow", BaseURL: "https://supplier.example", Protocol: "generic-http",
		Status: constants.ConnectionStatusActive, ExchangeRate: decimal.NewFromInt(1),
		PriceMarkupPercent: decimal.Zero, PriceRoundingMode: "none",
	}
	if err := db.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	category := categorydomain.Category{Slug: "workflow", NameJSON: jsonmap.JSON{"zh-CN": "测试"}, IsActive: true}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	provider := &workflowProvider{db: db, a: a}
	mappings, skus := mappingstore.NewMappingStore(db), mappingstore.NewSKUMappingStore(db)
	catalog, err := mappingapp.NewService(mappingapp.Options{
		Mappings: mappings, SKUMappings: skus, Products: productstore.NewProductStore(db),
		SKUs: productstore.NewSKUStore(db), Categories: categorystore.NewCategoryStore(db),
		Connections: provider, Media: workflowMedia{}, Transactions: workflowImportTransaction{db: db},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := procurementapp.NewService(procurementapp.Options{
		Repository:      procurementstore.New(db),
		Orders:          orderreader.New(orderstore.New(db, "workflow-only-guest-credential-secret")),
		ProductMappings: mappingreader.NewProducts(mappings), SKUMappings: mappingreader.NewSKUs(skus),
		Connections:    upstreamgateway.New(provider),
		OrderLifecycle: procurementstore.NewLifecycle(db, nil, nil, config.EmailConfig{}),
	})
	return &workflowFixture{db: db, adapter: a, catalog: catalog, procure: service, connection: connection.ID, category: category.ID}
}

func (f *workflowFixture) importProduct(t *testing.T, externalID, slug string) (*mappingdomain.Mapping, []mappingdomain.SKUMapping) {
	t.Helper()
	mapping, err := f.catalog.ImportUpstreamProductExternal(f.connection, externalID, f.category, slug, false)
	if err != nil {
		t.Fatalf("real catalog import: %v", err)
	}
	var stored mappingdomain.Mapping
	if err := f.db.First(&stored, mapping.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.UpstreamProductExternalID != externalID || stored.UpstreamProductID != 0 {
		t.Fatalf("product identity changed: %+v", stored)
	}
	skus, err := f.catalog.GetSKUMappings(mapping.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mapping, skus
}

func (f *workflowFixture) paidOrder(t *testing.T, mapping *mappingdomain.Mapping, sku mappingdomain.SKUMapping) (*orderdomain.Order, *procurementdomain.Order) {
	t.Helper()
	amount := sharedmoney.FromDecimal(decimal.RequireFromString("29.95"))
	order := &orderdomain.Order{
		OrderNo: "000-local/订单", UserID: 1, Status: constants.OrderStatusPaid,
		Currency: "CNY", OriginalAmount: amount, TotalAmount: amount,
	}
	if err := f.db.Create(order).Error; err != nil {
		t.Fatal(err)
	}
	item := orderdomain.OrderItem{
		OrderID: order.ID, ProductID: mapping.LocalProductID, SKUID: sku.LocalSKUID,
		Quantity: 1, FulfillmentType: constants.FulfillmentTypeUpstream,
		TitleJSON: jsonmap.JSON{"zh-CN": "测试"}, UnitPrice: amount, TotalPrice: amount,
	}
	if err := f.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.procure.CreateForOrder(order.ID); err != nil {
		t.Fatalf("payment procurement hook: %v", err)
	}
	procurement := f.loadProcurement(t, order.ID)
	var request procurementcontract.CreateOrderRequest
	if err := json.Unmarshal([]byte(procurement.RequestSnapshot), &request); err != nil {
		t.Fatal(err)
	}
	if request.ProductExternalID != mapping.UpstreamProductExternalID || request.SKUExternalID != sku.UpstreamSKUExternalID || request.SKUID != 0 || request.DownstreamOrderNo != order.OrderNo || request.Quantity != 1 || procurement.ConnectionSnapshot == "" {
		t.Fatalf("frozen request lost identity: %+v", request)
	}
	// A duplicate payment notification must not create a second procurement.
	if err := f.procure.CreateForOrder(order.ID); !errors.Is(err, procurementcontract.ErrExists) {
		t.Fatalf("duplicate payment hook: %v", err)
	}
	return order, procurement
}

func (f *workflowFixture) loadProcurement(t *testing.T, localID uint) *procurementdomain.Order {
	t.Helper()
	var order procurementdomain.Order
	if err := f.db.Where("local_order_id = ?", localID).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	return &order
}

func (f *workflowFixture) assertDelivered(t *testing.T, localID uint, externalID, upstreamNo, amount, currency, payload string) {
	t.Helper()
	procurement := f.loadProcurement(t, localID)
	if procurement.Status != constants.ProcurementStatusFulfilled || procurement.UpstreamPayload != payload || procurement.UpstreamOrderID != 0 || procurement.UpstreamOrderExternalID != externalID || procurement.UpstreamOrderNo != upstreamNo {
		t.Fatalf("persisted procurement/delivery mismatch: %+v", procurement)
	}
	if procurement.UpstreamAmount.String() != amount || procurement.UpstreamCurrency != currency || procurement.LocalSellAmount.String() != "29.95" || procurement.Currency != "CNY" {
		t.Fatalf("persisted amounts/currencies changed: %+v", procurement)
	}
	var local orderdomain.Order
	if err := f.db.First(&local, localID).Error; err != nil {
		t.Fatal(err)
	}
	if local.Status != constants.OrderStatusDelivered || local.TotalAmount.String() != "29.95" || local.Currency != "CNY" {
		t.Fatalf("local order mismatch: %+v", local)
	}
	var deliveries []fulfillmentdomain.Fulfillment
	if err := f.db.Where("order_id = ?", localID).Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Payload != payload || deliveries[0].Status != constants.FulfillmentStatusDelivered || deliveries[0].Type != constants.FulfillmentTypeUpstream || deliveries[0].DeliveredAt == nil {
		t.Fatalf("persisted fulfillment mismatch: %+v", deliveries)
	}
	var count int64
	if err := f.db.Model(&procurementdomain.Order{}).Where("local_order_id = ?", localID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate procurement: count=%d err=%v", count, err)
	}
}

func workflowNoResubmit(t *testing.T, f *workflowFixture, localID, procurementID uint) {
	t.Helper()
	for range 2 {
		if err := f.procure.SubmitToUpstream(procurementID); err != nil && !errors.Is(err, procurementcontract.ErrStatusInvalid) {
			t.Fatalf("duplicate submit: %v", err)
		}
		if err := f.procure.RetryManual(procurementID); err != nil && !errors.Is(err, procurementcontract.ErrStatusInvalid) {
			t.Fatalf("manual retry: %v", err)
		}
		if err := f.procure.CreateForOrder(localID); err != nil && !errors.Is(err, procurementcontract.ErrExists) && !errors.Is(err, procurementcontract.ErrStatusInvalid) {
			t.Fatalf("duplicate payment hook: %v", err)
		}
		f.procure.SyncAcceptedOrders()
		if err := f.procure.PollUpstreamStatus(procurementID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkflowAmbiguousCreateBlocksRefundAndNeverResubmits(t *testing.T) {
	for _, failure := range []string{"timeout", "500"} {
		t.Run(failure, func(t *testing.T) {
			f := newWorkflowFixture(t, func(httpintegration.Endpoint, map[string]string) (string, error) {
				return workflowSingleProduct, nil
			})
			mapping, skus := f.importProduct(t, "001", "ambiguous-product")
			local, proc := f.paidOrder(t, mapping, skus[0])
			creates, lookups := 0, 0
			f.adapter.execute = func(_ context.Context, _ string, ep httpintegration.Endpoint, _ map[string]string) (*httpintegration.Result, error) {
				switch ep.Path {
				case "/orders":
					creates++
					if failure == "timeout" {
						return nil, context.DeadlineExceeded
					}
					return &httpintegration.Result{StatusCode: 500}, &httpintegration.ExecutionError{Kind: httpintegration.Unknown}
				case "/lookup":
					lookups++
					return nil, context.DeadlineExceeded
				default:
					t.Fatalf("unexpected endpoint %s", ep.Path)
					return nil, nil
				}
			}
			if err := f.procure.SubmitToUpstream(proc.ID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_ = f.procure.SubmitToUpstream(proc.ID)
				_ = f.procure.RetryManual(proc.ID)
				_ = f.procure.PollUpstreamStatus(proc.ID)
				f.procure.SyncAcceptedOrders()
			}
			if saved := f.loadProcurement(t, local.ID); saved.Status != "unknown" || creates != 1 || lookups == 0 {
				t.Fatalf("ambiguous create replay/state: creates=%d lookups=%d state=%s", creates, lookups, saved.Status)
			}
			if err := f.procure.CancelManual(proc.ID); !errors.Is(err, procurementcontract.ErrStatusInvalid) {
				t.Fatalf("unknown cancellation allowed: %v", err)
			}
			err := orderstore.New(f.db, "workflow-only-guest-credential-secret").WithinTransaction(func(tx ordercontract.Transaction) error {
				return tx.Orders().(ordercontract.RefundGuard).LockRefundScope(local.ID)
			})
			if !errors.Is(err, ordercontract.ErrProcurementRefundBlocked) {
				t.Fatalf("ambiguous procurement refund allowed: %v", err)
			}
			var count int64
			if err := f.db.Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", local.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("ambiguous order delivered: count=%d err=%v", count, err)
			}
		})
	}
}

func TestWorkflowCancellationConfirmationAndRefusal(t *testing.T) {
	for _, status := range []string{"canceled", "paid", "timeout"} {
		t.Run(status, func(t *testing.T) {
			cancels := 0
			f := newWorkflowFixture(t, func(ep httpintegration.Endpoint, vars map[string]string) (string, error) {
				switch ep.Path {
				case "/products/{{product_id}}":
					return workflowSingleProduct, nil
				case "/orders":
					return asynchronous, nil
				case "/orders/{{order_id}}/cancel":
					cancels++
					if vars["order_id"] != " 00000000000000000001/X " {
						t.Fatalf("cancel identity changed: %q", vars["order_id"])
					}
					if status == "timeout" {
						return "", context.DeadlineExceeded
					}
					return `{"status":"` + status + `"}`, nil
				default:
					t.Fatalf("unexpected endpoint %s", ep.Path)
					return "", nil
				}
			})
			mapping, skus := f.importProduct(t, "001", "cancel-product")
			local, proc := f.paidOrder(t, mapping, skus[0])
			if err := f.procure.SubmitToUpstream(proc.ID); err != nil {
				t.Fatal(err)
			}
			err := f.procure.CancelManual(proc.ID)
			if (status == "canceled") != (err == nil) {
				t.Fatalf("cancellation result: %v", err)
			}
			if status == "paid" && !errors.Is(err, procurementcontract.ErrUpstreamCancelRefused) {
				t.Fatalf("refusal classification: %v", err)
			}
			want := map[string]string{"canceled": "canceled", "paid": "accepted", "timeout": "unknown"}[status]
			saved := f.loadProcurement(t, local.ID)
			if saved.Status != want || saved.CancellationPending != (status == "timeout") || cancels != 1 {
				t.Fatalf("cancel state=%s pending=%v calls=%d", saved.Status, saved.CancellationPending, cancels)
			}
			if status != "paid" {
				_ = f.procure.CancelManual(proc.ID)
				if cancels != 1 {
					t.Fatal("terminal/ambiguous cancellation replayed")
				}
			}
		})
	}
}

const workflowSingleProduct = `{"id":"001","title":"单规格","price_amount":"1.23","currency":"USD","stock_quantity":8,"is_active":true}`

func TestWorkflowSynchronousProductLevelDelivery(t *testing.T) {
	for _, orderID := range []struct{ name, raw, want string }{
		{"opaque", `" 000-order/订单 "`, " 000-order/订单 "},
		{"large_number", `900719925474099300001`, "900719925474099300001"},
	} {
		t.Run(orderID.name, func(t *testing.T) {
			creates, products := 0, 0
			const payload = " 000-card \nliteral-secret\n尾部卡密 "
			encoded, _ := json.Marshal(payload)
			f := newWorkflowFixture(t, func(ep httpintegration.Endpoint, vars map[string]string) (string, error) {
				switch ep.Path {
				case "/products/{{product_id}}":
					products++
					if vars["product_id"] != "001" {
						t.Fatalf("product ID %q", vars["product_id"])
					}
					return workflowSingleProduct, nil
				case "/orders":
					creates++
					if vars["product_id"] != "001" || vars["sku_id"] != "" || vars["quantity"] != "1" || vars["downstream_order_no"] != "000-local/订单" {
						t.Fatalf("single-SKU create variables: %+v", vars)
					}
					return fmt.Sprintf(`{"order_id":%s,"order_no":"000-upstream","status":"delivered","amount":"1.23","currency":"USD","payload":%s}`, orderID.raw, encoded), nil
				default:
					t.Fatalf("unexpected endpoint %s", ep.Path)
					return "", nil
				}
			})
			mapping, skus := f.importProduct(t, "001", "single-product")
			if len(skus) != 1 || !skus[0].ProductLevel || skus[0].UpstreamSKUExternalID != "" || skus[0].UpstreamSKUID != 0 {
				t.Fatalf("synthetic SKU mapping: %+v", skus)
			}
			order, procurement := f.paidOrder(t, mapping, skus[0])
			if err := f.procure.SubmitToUpstream(procurement.ID); err != nil {
				t.Fatal(err)
			}
			f.assertDelivered(t, order.ID, orderID.want, "000-upstream", "1.23", "USD", payload)
			workflowNoResubmit(t, f, order.ID, procurement.ID)
			f.assertDelivered(t, order.ID, orderID.want, "000-upstream", "1.23", "USD", payload)
			if creates != 1 || products != 1 {
				t.Fatalf("HTTP calls: create=%d product=%d", creates, products)
			}
		})
	}
}

func TestWorkflowPaginatedMultiSKUImportAndAsyncDelivery(t *testing.T) {
	const multi = `{"id":"P/多规格","title":"多规格","currency":"USD","is_active":true,"skus":[{"id":"001","price_amount":"2.35","stock_quantity":4,"is_active":true},{"id":"1","price_amount":"4.70","stock_quantity":6,"is_active":true}]}`
	const externalOrder = "18446744073709551616001"
	const payload = "first-card\n 000-second "
	lists, products, creates, polls := 0, 0, 0, 0
	f := newWorkflowFixture(t, func(ep httpintegration.Endpoint, vars map[string]string) (string, error) {
		switch ep.Path {
		case "/products":
			lists++
			if lists == 1 {
				if vars["page"] != "1" || vars["offset"] != "0" || vars["cursor"] != "" {
					t.Fatalf("first page: %+v", vars)
				}
				return `{"data":[` + workflowSingleProduct + `],"has_more":true,"next_cursor":" 000-next "}`, nil
			}
			if lists != 2 || vars["page"] != "2" || vars["offset"] != "1" || vars["cursor"] != " 000-next " {
				t.Fatalf("next page: %+v", vars)
			}
			return `{"data":[` + multi + `],"has_more":false}`, nil
		case "/products/{{product_id}}":
			products++
			if vars["product_id"] != "P/多规格" {
				t.Fatalf("get product: %+v", vars)
			}
			return multi, nil
		case "/orders":
			creates++
			if vars["product_id"] != "P/多规格" || vars["sku_id"] != "001" {
				t.Fatalf("multi-SKU create: %+v", vars)
			}
			return `{"order_id":18446744073709551616001,"order_no":"000-async","status":"paid","amount":"2.35","currency":"USD"}`, nil
		case "/orders/{{order_id}}":
			polls++
			if vars["order_id"] != externalOrder {
				t.Fatalf("poll ID: %+v", vars)
			}
			if polls == 1 {
				return `{"order_id":18446744073709551616001,"status":"fulfilling","amount":"2.35","currency":"USD"}`, nil
			}
			return `{"order_id":18446744073709551616001,"status":"delivered","amount":"2.35","currency":"USD","cards":["first-card"," 000-second "]}`, nil
		default:
			t.Fatalf("unexpected endpoint %s", ep.Path)
			return "", nil
		}
	})
	first, err := f.catalog.ListUpstreamProductsWithOptions(f.connection, upstream.ListProductsOpts{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalKnown || len(first.Items) != 1 || first.Items[0].ExternalID != "001" || first.HasMore == nil || !*first.HasMore || first.NextCursor != " 000-next " {
		t.Fatalf("first page: %+v", first)
	}
	second, err := f.catalog.ListUpstreamProductsWithOptions(f.connection, upstream.ListProductsOpts{Page: 2, PageSize: 1, Offset: len(first.Items), Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore == nil || *second.HasMore || second.Items[0].ExternalID != "P/多规格" {
		t.Fatalf("second page: %+v", second)
	}
	mapping, skus := f.importProduct(t, second.Items[0].ExternalID, "multi-product")
	if len(skus) != 2 {
		t.Fatalf("SKU count: %+v", skus)
	}
	byID := make(map[string]mappingdomain.SKUMapping)
	for _, sku := range skus {
		if sku.ProductLevel || sku.UpstreamSKUID != 0 {
			t.Fatalf("SKU identity: %+v", sku)
		}
		byID[sku.UpstreamSKUExternalID] = sku
	}
	if byID["001"].LocalSKUID == 0 || byID["1"].LocalSKUID == 0 || byID["001"].LocalSKUID == byID["1"].LocalSKUID {
		t.Fatalf("collapsed SKU IDs: %+v", skus)
	}
	for id, price := range map[string]string{"001": "2.35", "1": "4.70"} {
		if byID[id].UpstreamPrice.String() != price || byID[id].UpstreamStock != map[string]int{"001": 4, "1": 6}[id] {
			t.Fatalf("persisted upstream SKU price/stock %s: %+v", id, byID[id])
		}
		var sku productdomain.ProductSKU
		if err := f.db.First(&sku, byID[id].LocalSKUID).Error; err != nil {
			t.Fatal(err)
		}
		if sku.PriceAmount.String() != price || sku.CostPriceAmount.String() != price {
			t.Fatalf("imported price %s: %+v", id, sku)
		}
	}
	order, procurement := f.paidOrder(t, mapping, byID["001"])
	if err := f.procure.SubmitToUpstream(procurement.ID); err != nil {
		t.Fatal(err)
	}
	accepted := f.loadProcurement(t, order.ID)
	if accepted.Status != constants.ProcurementStatusAccepted || accepted.UpstreamOrderExternalID != externalOrder || accepted.UpstreamAmount.String() != "2.35" || accepted.UpstreamCurrency != "USD" {
		t.Fatalf("async acceptance: %+v", accepted)
	}
	for poll := 0; poll < 2; poll++ {
		if poll == 0 {
			var deliveries int64
			if err := f.db.Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&deliveries).Error; err != nil || deliveries != 0 {
				t.Fatalf("async acceptance delivered prematurely: count=%d err=%v", deliveries, err)
			}
			if err := f.procure.SubmitToUpstream(procurement.ID); !errors.Is(err, procurementcontract.ErrStatusInvalid) {
				t.Fatalf("accepted order must not submit again: %v", err)
			}
		}
		if err := f.procure.PollUpstreamStatus(procurement.ID); err != nil {
			t.Fatal(err)
		}
		if poll == 0 && f.loadProcurement(t, order.ID).Status != constants.ProcurementStatusAccepted {
			t.Fatal("non-delivered upstream poll must remain accepted")
		}
	}
	f.assertDelivered(t, order.ID, externalOrder, "000-async", "2.35", "USD", payload)
	workflowNoResubmit(t, f, order.ID, procurement.ID)
	if lists != 2 || products != 1 || creates != 1 || polls != 2 {
		t.Fatalf("calls list=%d get=%d create=%d poll=%d", lists, products, creates, polls)
	}
}

func TestWorkflowTimeoutLookupDeliversWithoutResubmission(t *testing.T) {
	for _, recovery := range []string{"manual", "scan", "submit"} {
		t.Run(recovery, func(t *testing.T) {
			creates, lookups := 0, 0
			const payload = " 000-recovered \nliteral-secret "
			encoded, _ := json.Marshal(payload)
			f := newWorkflowFixture(t, func(ep httpintegration.Endpoint, vars map[string]string) (string, error) {
				switch ep.Path {
				case "/products/{{product_id}}":
					return workflowSingleProduct, nil
				case "/orders":
					creates++
					if creates != 1 {
						t.Fatal("ambiguous create was resubmitted and could charge twice")
					}
					return "", context.DeadlineExceeded
				case "/lookup":
					lookups++
					if vars["downstream_order_no"] != "000-local/订单" {
						t.Fatalf("lookup identity: %+v", vars)
					}
					return fmt.Sprintf(`{"order_id":" 000-recovered/订单 ","status":"delivered","amount":"1.23","currency":"USD","payload":%s}`, encoded), nil
				default:
					t.Fatalf("unexpected endpoint %s", ep.Path)
					return "", nil
				}
			})
			mapping, skus := f.importProduct(t, "001", "timeout-product")
			order, procurement := f.paidOrder(t, mapping, skus[0])
			if err := f.procure.SubmitToUpstream(procurement.ID); err != nil {
				t.Fatal(err)
			}
			unknown := f.loadProcurement(t, order.ID)
			if unknown.Status != "unknown" || unknown.UpstreamOrderExternalID != "" || unknown.RequestSnapshot == "" {
				t.Fatalf("timeout must remain ambiguous: %+v", unknown)
			}
			var err error
			switch recovery {
			case "manual":
				err = f.procure.RetryManual(procurement.ID)
			case "scan":
				f.procure.SyncAcceptedOrders()
			case "submit":
				err = f.procure.SubmitToUpstream(procurement.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			f.assertDelivered(t, order.ID, " 000-recovered/订单 ", "", "1.23", "USD", payload)
			workflowNoResubmit(t, f, order.ID, procurement.ID)
			f.assertDelivered(t, order.ID, " 000-recovered/订单 ", "", "1.23", "USD", payload)
			if creates != 1 || lookups != 1 {
				t.Fatalf("calls create=%d lookup=%d", creates, lookups)
			}
		})
	}
}
