package telegrambot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/config"
	"github.com/XManCodeing/XMall/internal/constants"
	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"
	productapp "github.com/XManCodeing/XMall/internal/modules/catalog/product/application"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	productgorm "github.com/XManCodeing/XMall/internal/modules/catalog/product/store/gormstore"
	coupon "github.com/XManCodeing/XMall/internal/modules/coupon/domain"
	coupongorm "github.com/XManCodeing/XMall/internal/modules/coupon/infrastructure/gormstore"
	fulfillment "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	externalidentity "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	identitygorm "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/infrastructure/gormstore"
	user "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	usergorm "github.com/XManCodeing/XMall/internal/modules/identity/user/infrastructure/gormstore"
	userauth "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	userauthgorm "github.com/XManCodeing/XMall/internal/modules/identity/userauth/infrastructure/gormstore"
	app "github.com/XManCodeing/XMall/internal/modules/order/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	order "github.com/XManCodeing/XMall/internal/modules/order/domain"
	ordergorm "github.com/XManCodeing/XMall/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/XManCodeing/XMall/internal/modules/payment/application"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	paymentgorm "github.com/XManCodeing/XMall/internal/modules/payment/infrastructure/gormstore"
	promotion "github.com/XManCodeing/XMall/internal/modules/promotion/domain"
	promotiongorm "github.com/XManCodeing/XMall/internal/modules/promotion/infrastructure/gormstore"
	settings "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	walletapp "github.com/XManCodeing/XMall/internal/modules/wallet/application"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	walletdomain "github.com/XManCodeing/XMall/internal/modules/wallet/domain"
	walletgorm "github.com/XManCodeing/XMall/internal/modules/wallet/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestPaymentDeadlineUsesOrderNotExpirationEvent(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	p := paymentdomain.Payment{ExpiredAt: &past}
	o := order.Order{OrderNo: "ORDER-123", ExpiresAt: &deadline}
	got := shopPayment(&p, &o, "text")
	if got.OrderNumber != o.OrderNo || got.ExpiresAt != o.ExpiresAt {
		t.Fatalf("wrong payment facts: %+v", got)
	}
	o.ExpiresAt = nil
	if shopPayment(&p, &o, "text").ExpiresAt != nil {
		t.Fatal("expired_at became a deadline")
	}
}

func TestResolvePaymentRawPayloads(t *testing.T) {
	for _, raw := range []string{"weixin://wxpay/bizpayurl?pr=test", "https://pay.example/scan", "https://pay.example/pay.png"} {
		calls := 0
		p, err := resolvePaymentMedia(context.Background(), &c.Payment{QRCode: raw}, func(context.Context, string) ([]byte, error) {
			calls++
			return nil, errors.New("403 / redirect / timeout must not affect raw payload")
		})
		if err != nil || p.QRCode != raw || len(p.QRImage) != 0 {
			t.Fatalf("payment=%+v err=%v", p, err)
		}
		if calls != 0 {
			t.Fatal("payment URI must not be fetched")
		}
		photo, _, err := shopapp.PaymentMedia(*p)
		if err != nil || len(photo) == 0 {
			t.Fatalf("missing locally encoded QR: %v", err)
		}
	}
	for _, raw := range []string{"https://pay.example/scan", "http://pay.example/scan"} {
		p, err := resolvePaymentMedia(context.Background(), &c.Payment{InteractionMode: "qr", PayURL: raw}, func(context.Context, string) ([]byte, error) { t.Fatal("raw PayURL fetched"); return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		photo, _, err := shopapp.PaymentMedia(*p)
		if err != nil || len(photo) == 0 {
			t.Fatalf("raw PayURL unavailable: %v", err)
		}
	}
}

func TestExplicitImageDoesNotFallBackToEncodingNonImage(t *testing.T) {
	for _, body := range [][]byte{nil, {}} {
		_, err := resolvePaymentMedia(context.Background(), &c.Payment{QRCodeKind: "image", QRCode: "https://pay.example/image"}, func(context.Context, string) ([]byte, error) { return body, nil })
		if !errors.Is(err, c.ErrUnavailable) { t.Fatalf("non-image response accepted: %v", err) }
	}
}

func TestResolvePaymentMedia(t *testing.T) {
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	for _, tt := range []struct {
		name                         string
		payment                      c.Payment
		fetchErr                     bool
		wantPhoto, wantLink, wantErr bool
	}{
		{"image only", c.Payment{QRCode: "https://pay.example/qr?id=1"}, false, true, false, false},
		{"image in PayURL", c.Payment{InteractionMode: "qr", PayURL: "https://pay.example/qr"}, false, true, false, false},
		{"image plus checkout", c.Payment{QRCode: "https://pay.example/qr", PayURL: "https://pay.example/checkout"}, false, true, true, false},
		{"fallback", c.Payment{QRCode: "https://pay.example/qr", PayURL: "https://pay.example/checkout"}, true, false, true, false},
		{"failure without checkout", c.Payment{QRCode: "https://pay.example/qr"}, true, false, false, true},
		{"failure with identical checkout", c.Payment{InteractionMode: "qr", PayURL: "https://pay.example/qr"}, true, false, false, true},
		{"redirect skips fetch", c.Payment{InteractionMode: "redirect", PayURL: "https://pay.example/checkout", QRCode: "https://pay.example/qr"}, true, false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			originalMode := tt.payment.InteractionMode
			tt.payment.QRCodeKind = "image"
			p, err := resolvePaymentMedia(context.Background(), &tt.payment, func(context.Context, string) ([]byte, error) {
				calls++
				if tt.fetchErr {
					return nil, errors.New("unavailable")
				}
				return imageData.Bytes(), nil
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if originalMode == "redirect" && calls != 0 {
				t.Fatal("redirect fetched")
			}
			if err != nil {
				return
			}
			photo, link, err := shopapp.PaymentMedia(*p)
			if err != nil || (len(photo) > 0) != tt.wantPhoto || (link != "") != tt.wantLink {
				t.Fatalf("photo=%d link=%q err=%v", len(photo), link, err)
			}
			if tt.wantPhoto && !bytes.Equal(photo, imageData.Bytes()) {
				t.Fatal("image URL was encoded instead of original media")
			}
		})
	}
}

type shopCatalogStocks struct {
	mapping *mappingdomain.Mapping
	rows    []mappingdomain.SKUMapping
	err     error
}

func (s shopCatalogStocks) GetByLocalProductID(uint) (*mappingdomain.Mapping, error) {
	return s.mapping, s.err
}
func (s shopCatalogStocks) GetSKUMappings(uint) ([]mappingdomain.SKUMapping, error) {
	return s.rows, s.err
}

func TestShopProductUsesPublicVisibility(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:shop-catalog-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err = db.AutoMigrate(&user.User{}, &externalidentity.Identity{}, &category.Category{}, &product.Product{}, &product.ProductSKU{}); err != nil {
		t.Fatal(err)
	}
	u := user.User{Email: "catalog@example.test", PasswordHash: "test", Status: "active"}
	if err = db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	identity := externalidentity.Identity{UserID: u.ID, Provider: "telegram", ProviderUserID: "123"}
	if err = db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	cat := category.Category{Slug: "catalog", IsActive: true, NameJSON: jsonmap.JSON{"en-US": "Catalog"}}
	if err = db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}
	p := product.Product{Slug: "catalog", TitleJSON: jsonmap.JSON{"en-US": "Product"}, CategoryID: cat.ID, IsActive: true, FulfillmentType: constants.FulfillmentTypeManual, PriceAmount: money.FromDecimal(decimal.NewFromInt(10)), StockDisplayMode: "range"}
	if err = db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	sku := product.ProductSKU{ProductID: p.ID, SKUCode: "visible", IsActive: true, ManualStockTotal: 17, PriceAmount: money.FromDecimal(decimal.NewFromInt(12))}
	if err = db.Create(&sku).Error; err != nil {
		t.Fatal(err)
	}
	repo := productgorm.NewProductStore(db)
	auth := userauth.NewService(&config.Config{}, usergorm.New(db), identitygorm.New(db), nil, nil, nil, nil)
	auth.SetAuthUnitOfWork(userauthgorm.New(db))
	a := &shopAdapter{d: ShopDependencies{Auth: auth, Products: productapp.NewService(productapp.Options{Products: repo}), Currency: func(string) (string, error) { return "CNY", nil }}}
	actor := c.Actor{BotID: 7, TelegramID: 123, UserID: u.ID, Locale: "en-US"}
	v, err := a.Product(context.Background(), actor, p.ID)
	if err != nil || v.Price != "10.00" || v.Currency != "CNY" || len(v.SKUs) != 1 || v.SKUs[0].Stock != "Stock: 6–20" {
		t.Fatalf("detail: %+v %v", v, err)
	}
	if err = db.Model(&sku).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	v, err = a.Product(context.Background(), actor, p.ID)
	if err != nil || len(v.SKUs) != 0 {
		t.Fatalf("inactive SKU exposed: %+v %v", v, err)
	}
	if err = db.Model(&cat).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.Product(context.Background(), actor, p.ID); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("inactive category exposed: %v", err)
	}
	if err = db.Model(&cat).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&p).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = a.Product(context.Background(), actor, p.ID); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("inactive product exposed: %v", err)
	}
}

func TestProductViewPricesAndStockPolicy(t *testing.T) {
	p := product.Product{PriceAmount: money.FromDecimal(decimal.NewFromInt(15)), FulfillmentType: constants.FulfillmentTypeManual, ManualStockTotal: 999, SKUs: []product.ProductSKU{
		{ID: 1, IsActive: true, SKUCode: "A", PriceAmount: money.FromDecimal(decimal.NewFromInt(20)), ManualStockTotal: 17},
		{ID: 2, IsActive: false, ManualStockTotal: 500},
	}}
	for _, tc := range []struct{ mode, want string }{
		{"exact", "Stock: 17"}, {"status", "In stock"}, {"range", "Stock: 6–20"}, {"hidden", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p.StockDisplayMode = tc.mode
			v := productView(p, "en-US")
			if v.Price != "15.00" || len(v.SKUs) != 1 || v.SKUs[0].Price != "20.00" || v.Stock != tc.want || v.SKUs[0].Stock != tc.want {
				t.Fatalf("wrong public price/stock: %+v", v)
			}
		})
	}
	p.StockDisplayMode = "exact"
	p.FulfillmentType = constants.FulfillmentTypeAuto
	p.SKUs[0].AutoStockAvailable = 3
	if v := productView(p, "en-US"); v.Stock != "Stock: 3" || v.SKUs[0].Stock != "Stock: 3" {
		t.Fatalf("auto stock: %+v", v)
	}
}

func TestProductViewCachedUpstreamStockNeverInventsAvailability(t *testing.T) {
	p := product.Product{FulfillmentType: constants.FulfillmentTypeUpstream, ManualStockTotal: -1, SKUs: []product.ProductSKU{{ID: 1, IsActive: true, ManualStockTotal: -1}}}
	for _, tc := range []struct {
		name   string
		stocks shopCatalogStocks
		want   string
	}{
		{"missing", shopCatalogStocks{}, "Stock unavailable"},
		{"error", shopCatalogStocks{err: errors.New("unavailable")}, "Stock unavailable"},
		{"empty", shopCatalogStocks{mapping: &mappingdomain.Mapping{ID: 1, IsActive: true}}, "Out of stock"},
		{"inactive", shopCatalogStocks{mapping: &mappingdomain.Mapping{ID: 1, IsActive: true}, rows: []mappingdomain.SKUMapping{{LocalSKUID: 1, UpstreamStock: 99}}}, "Out of stock"},
		{"cached", shopCatalogStocks{mapping: &mappingdomain.Mapping{ID: 1, IsActive: true}, rows: []mappingdomain.SKUMapping{{LocalSKUID: 1, UpstreamIsActive: true, UpstreamStock: 8}}}, "Stock: 8"},
		{"unlimited", shopCatalogStocks{mapping: &mappingdomain.Mapping{ID: 1, IsActive: true}, rows: []mappingdomain.SKUMapping{{LocalSKUID: 1, UpstreamIsActive: true, UpstreamStock: -1}}}, "Unlimited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := productView(p, "en-US", &ShopDependencies{ProductMappings: tc.stocks, SKUStocks: tc.stocks})
			if v.Stock != tc.want || v.SKUs[0].Stock != tc.want {
				t.Fatalf("upstream stock: %+v", v)
			}
		})
	}
}

type contentSettingsStub struct {
	cfg settings.TelegramBotConfigSetting
}

func (s contentSettingsStub) GetTelegramBotConfig() (settings.TelegramBotConfigSetting, error) {
	return s.cfg, nil
}
func TestContentOmitsUnsupportedMenuAndUsesLocale(t *testing.T) {
	cfg := settings.DefaultTelegramBotConfig()
	cfg.Welcome.Enabled = true
	cfg.Welcome.Message = settings.LocalizedText{"en-US": "Configured welcome"}
	cfg.Menu.Items = []settings.TelegramBotMenuItem{
		{Enabled: true, Order: 1, Label: settings.LocalizedText{"en-US": "Buy"}, Action: settings.TelegramBotMenuAction{Type: "builtin", Value: "shop"}},
		{Enabled: true, Order: 3, Label: settings.LocalizedText{"en-US": "Not implemented"}, Action: settings.TelegramBotMenuAction{Type: "builtin", Value: "giftcard"}},
	}
	content, err := (shopContent{settings: contentSettingsStub{cfg}}).Content(context.Background(), c.Actor{Locale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if content.Welcome != "Configured welcome" || len(content.Menu) != 1 || content.Menu[0].Action != "catalog" {
		t.Fatalf("bad content: %+v", content)
	}
}

func TestContentRequiresSupportedActionType(t *testing.T) {
	for _, actionType := range []string{"web_app", "command", "unknown", ""} {
		t.Run(actionType, func(t *testing.T) {
			cfg := settings.DefaultTelegramBotConfig()
			cfg.Menu.Items = []settings.TelegramBotMenuItem{
				{Enabled: true, Action: settings.TelegramBotMenuAction{Type: actionType, Value: "shop"}},
				{Enabled: true, Action: settings.TelegramBotMenuAction{Type: "url", Value: "https://example.com"}},
				{Enabled: false, Action: settings.TelegramBotMenuAction{Type: "builtin", Value: "orders"}},
			}
			content, err := (shopContent{settings: contentSettingsStub{cfg}}).Content(context.Background(), c.Actor{Locale: "en-US"})
			if err != nil || len(content.Menu) != 1 || content.Menu[0].URL != "https://example.com" || content.Menu[0].Action != "" {
				t.Fatalf("unsupported type mapped to builtin: %+v, %v", content.Menu, err)
			}
		})
	}
}

type shopQueue struct{}

// Embedded unimplemented wallet methods panic if a read accidentally mutates.
type shopReadWallet struct {
	walletcontract.UseCase
	ids    []uint
	filter walletcontract.TransactionListFilter
	rows   []walletdomain.Transaction
	total  int64
}

func (w *shopReadWallet) GetBalancesByUserIDs(ids []uint) (map[uint]money.Amount, error) {
	w.ids = append([]uint(nil), ids...)
	return map[uint]money.Amount{}, nil
}
func (w *shopReadWallet) ListTransactions(filter walletcontract.TransactionListFilter) ([]walletdomain.Transaction, int64, error) {
	w.filter = filter
	return w.rows, w.total, nil
}

func TestShopIdentityLifetimeAndHistoryReadOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:shop-identity-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.AutoMigrate(&user.User{}, &externalidentity.Identity{}, &userauthdomain.TelegramHistoryGrant{}, &order.Order{}, &order.OrderItem{}, &fulfillment.Fulfillment{}); err != nil {
		t.Fatal(err)
	}
	current := user.User{Email: "current@example.test", PasswordHash: "test", Status: "active"}
	previous := user.User{Email: "previous@example.test", PasswordHash: "test", Status: "active"}
	for _, u := range []*user.User{&current, &previous} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	identity := externalidentity.Identity{UserID: current.ID, Provider: "telegram", ProviderUserID: "123"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	grant := userauthdomain.TelegramHistoryGrant{BotID: 7, TelegramUserID: "123", PreviousUserID: previous.ID, CurrentUserID: current.ID, IdentityID: identity.ID, Status: userauthdomain.TelegramHistoryActive}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	historical := order.Order{UserID: previous.ID, OrderNo: "HISTORY", Status: "pending_payment", Currency: "CNY"}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	users := usergorm.New(db)
	auth := userauth.NewService(&config.Config{}, users, identitygorm.New(db), nil, nil, nil, nil)
	auth.SetAuthUnitOfWork(userauthgorm.New(db))
	wallet := &shopReadWallet{}
	a := &shopAdapter{d: ShopDependencies{Auth: auth, Users: users, Wallet: wallet, Currency: func(s string) (string, error) { return s, nil }, Orders: app.NewOrderService(app.OrderServiceOptions{OrderStore: ordergorm.New(db, "test")})}}
	actor := c.Actor{BotID: 7, TelegramID: 123, UserID: current.ID}
	actor.Generation, err = a.Generation(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.readOwner(context.Background(), actor, previous.ID); err != nil {
		t.Fatalf("history read: %v", err)
	}
	account, err := a.Account(context.Background(), actor)
	if err != nil || account.Balance != "0.00" || len(wallet.ids) != 1 || wallet.ids[0] != current.ID {
		t.Fatalf("current account must be pure read: %+v %v", account, err)
	}
	account, err = a.HistoryAccount(context.Background(), actor, previous.ID)
	if err != nil || account.Balance != "0.00" || account.Email != "" || len(wallet.ids) != 1 || wallet.ids[0] != previous.ID {
		t.Fatalf("historical wallet must be scoped pure read: %+v %v", account, err)
	}
	for _, owner := range []uint{0, current.ID, previous.ID + 100} {
		if _, err := a.HistoryAccount(context.Background(), actor, owner); !errors.Is(err, c.ErrDenied) {
			t.Fatalf("unexpected history owner %d: %v", owner, err)
		}
	}
	wallet.rows = []walletdomain.Transaction{{UserID: previous.ID, Type: constants.WalletTxnTypeOrderRefund, Amount: money.FromDecimal(decimal.NewFromInt(2)), Currency: "CNY", Reference: "refund-only"}}
	wallet.total = 13
	refunds, more, err := a.HistoryRefunds(context.Background(), actor, previous.ID, 2)
	if err != nil || !more || len(refunds) != 1 || refunds[0].Reference != "refund-only" || wallet.filter.UserID != previous.ID || wallet.filter.Type != constants.WalletTxnTypeOrderRefund || wallet.filter.Page != 2 || wallet.filter.PageSize != 6 {
		t.Fatalf("refund scope/pagination: %+v %+v %v", refunds, wallet.filter, err)
	}
	wallet.rows[0].UserID = current.ID
	if _, _, err := a.HistoryRefunds(context.Background(), actor, previous.ID, 1); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("cross-account refund exposed: %v", err)
	}
	for _, page := range []int{0, -1, 10001} {
		if _, _, err := a.HistoryRefunds(context.Background(), actor, previous.ID, page); !errors.Is(err, c.ErrDenied) {
			t.Fatalf("invalid history page: %v", err)
		}
	}
	// Payments must use current ownership, never the historical read grant.
	if _, err := a.Pay(context.Background(), actor, historical.ID, 1, "pay"); err == nil {
		t.Fatal("history payment accepted")
	}
	missing := actor
	missing.Generation = ""
	if _, err := a.Pay(context.Background(), missing, historical.ID, 1, "pay"); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("missing generation: %v", err)
	}
	if err := db.Delete(&identity).Error; err != nil {
		t.Fatal(err)
	}
	identity.ID = 0
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.authorize(context.Background(), actor); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("stale lifetime: %v", err)
	}
	if _, err := a.HistoryAccount(context.Background(), actor, previous.ID); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("obsolete binding read historical balance: %v", err)
	}
	if _, _, err := a.HistoryRefunds(context.Background(), actor, previous.ID, 1); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("obsolete binding read historical refunds: %v", err)
	}
	actor.Generation, err = a.Generation(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.readOwner(context.Background(), actor, previous.ID); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("old history grant revived: %v", err)
	}
}

func TestShopProductFormsAndInactiveSKUs(t *testing.T) {
	p := product.Product{ID: 1, TitleJSON: jsonmap.JSON{"zh-CN": "商品", "en-US": "Product"}, ManualFormSchemaJSON: jsonmap.JSON{"fields": []interface{}{map[string]interface{}{"name": "email"}}}, SKUs: []product.ProductSKU{{ID: 1, IsActive: false}, {ID: 2, IsActive: true, SKUCode: "SKU"}}}
	v := productView(p, "en-US")
	if !v.Unsupported || v.Title != "Product" || v.Min != 1 || len(v.SKUs) != 1 || v.SKUs[0].ID != 2 {
		t.Fatalf("unsafe product view: %+v", v)
	}
	p.ManualFormSchemaJSON = jsonmap.JSON{"fields": []interface{}{}}
	if productView(p, "zh-CN").Unsupported {
		t.Fatal("empty form blocked")
	}
}
func TestShopOrderOnlyReadsDeliveredPayload(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	o := order.Order{ID: 1, UserID: 2, Status: "pending_payment", ExpiresAt: &expired, Fulfillment: &fulfillment.Fulfillment{Status: "pending", Payload: "NOT_DELIVERED"}, Children: []order.Order{{ID: 3, UserID: 2, Fulfillment: &fulfillment.Fulfillment{Status: "delivered", Payload: "EXISTING_SECRET"}}}}
	v := orderView(&o)
	if v.Pending || len(v.Delivered) != 1 || v.Delivered[0] != "EXISTING_SECRET" {
		t.Fatalf("unsafe order view: %+v", v)
	}
}

func (shopQueue) Enabled() bool                                  { return true }
func (shopQueue) EnqueueTimeoutCancel(uint, time.Duration) error { return nil }
func (shopQueue) EnqueueStatusEmail(uint, string) error          { return nil }

func TestShopTransactionSourceAndSelectionFingerprint(t *testing.T) {
	actor := c.Actor{BotID: 7, TelegramID: 123, UserID: 2, Generation: "3:2"}
	quote := c.Quote{Amount: "10", Currency: "CNY", Fingerprint: "same-quote"}
	selection := c.Selection{ProductID: 1, SKUID: 1, Quantity: 1}
	first, err := shopCreateInput(actor, selection, quote, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.Source.ExpectedIdentityID != 3 {
		t.Fatalf("missing identity guard: %+v", first.Source)
	}
	if err := delivery.GuardIdentity(struct{}{}, actor.UserID, first.Source); !errors.Is(err, delivery.ErrIdentityChanged) {
		t.Fatalf("missing transaction guard accepted: %v", err)
	}
	selection.SKUID++
	second, err := shopCreateInput(actor, selection, quote, "same-key")
	if err != nil || first.RequestHash == second.RequestHash {
		t.Fatalf("selection omitted from fingerprint: %v", err)
	}
	for _, generation := range []string{"", "3", "0:2", "3:9", "3:2:extra", "-1:2"} {
		actor.Generation = generation
		if _, err := shopCreateInput(actor, selection, quote, "same-key"); !errors.Is(err, c.ErrDenied) {
			t.Fatalf("invalid generation %q accepted: %v", generation, err)
		}
	}
}

// Inject a binding replacement after the optimistic replay check but before
// the actual order transaction, reproducing the authorize/write TOCTOU.
type shopBindingRaceStore struct {
	ordercontract.Store
	transactions int
	beforeWrite  func() error
}

func (s *shopBindingRaceStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	s.transactions++
	if s.transactions == 2 {
		if err := s.beforeWrite(); err != nil {
			return err
		}
	}
	return s.Store.WithinTransaction(fn)
}

func TestShopRealOrderExpectedPriceAndReplay(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(fmt.Sprintf("file:shop-order-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { sql.Close() })
	if e = db.AutoMigrate(&user.User{}, &externalidentity.Identity{}, &category.Category{}, &product.Product{}, &product.ProductSKU{}, &order.Order{}, &order.OrderItem{}, &fulfillment.Fulfillment{}, &coupon.Coupon{}, &coupon.CouponUsage{}, &promotion.Promotion{}, &delivery.OrderOperation{}, &delivery.OrderSource{}, &delivery.Event{}); e != nil {
		t.Fatal(e)
	}
	amount := money.FromDecimal(decimal.NewFromInt(10))
	cat := category.Category{Slug: "shop-test", NameJSON: jsonmap.JSON{"zh-CN": "test"}, IsActive: true}
	if e = db.Create(&cat).Error; e != nil {
		t.Fatal(e)
	}
	p := product.Product{CategoryID: cat.ID, Slug: "shop-test", TitleJSON: jsonmap.JSON{"zh-CN": "test"}, IsActive: true, PurchaseType: "member", FulfillmentType: "manual", PriceAmount: amount}
	if e = db.Create(&p).Error; e != nil {
		t.Fatal(e)
	}
	sku := product.ProductSKU{ProductID: p.ID, SKUCode: "DEFAULT", IsActive: true, PriceAmount: amount, ManualStockTotal: 20}
	if e = db.Create(&sku).Error; e != nil {
		t.Fatal(e)
	}
	u := user.User{Email: "shop@example.test", PasswordHash: "test", Status: "active"}
	if e = db.Create(&u).Error; e != nil {
		t.Fatal(e)
	}
	service := app.NewOrderService(app.OrderServiceOptions{OrderStore: ordergorm.New(db, "test-key").EnableBusinessEvents(), UserStore: usergorm.New(db), ProductStore: productgorm.NewProductStore(db), ProductSKUStore: productgorm.NewSKUStore(db), CouponStore: coupongorm.New(db), CouponUsageStore: coupongorm.NewUsageStore(db), PromotionRepo: promotiongorm.New(db), Queue: shopQueue{}})
	identity := externalidentity.Identity{UserID: u.ID, Provider: "telegram", ProviderUserID: "123"}
	if e = db.Create(&identity).Error; e != nil {
		t.Fatal(e)
	}
	actor := c.Actor{BotID: 7, TelegramID: 123, UserID: u.ID, Generation: fmt.Sprintf("%d:%d", identity.ID, u.ID)}
	selection := c.Selection{ProductID: p.ID, SKUID: sku.ID, Quantity: 2}
	preview, e := service.PreviewOrder(orderInput(actor, selection))
	if e != nil {
		t.Fatal(e)
	}
	q := c.Quote{Amount: preview.TotalAmount.String(), Currency: preview.Currency, Fingerprint: "quote-1"}
	input, e := shopCreateInput(actor, selection, q, "tg:7:checkout:1")
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&sku).Update("price_amount", 12).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.CreateOrder(input); !errors.Is(e, app.ErrInvalidOrderAmount) {
		t.Fatalf("price change accepted: %v", e)
	}
	var count int64
	db.Model(&order.Order{}).Count(&count)
	if count != 0 {
		t.Fatal("changed quote wrote order")
	}
	db.Model(&delivery.OrderOperation{}).Count(&count)
	if count != 0 {
		t.Fatal("changed quote reserved operation")
	}
	if e = db.First(&sku, sku.ID).Error; e != nil {
		t.Fatal(e)
	}
	if sku.ManualStockLocked != 0 {
		t.Fatal("changed quote locked stock")
	}
	q.Amount = "24.00"
	q.Fingerprint = "quote-2"
	input, e = shopCreateInput(actor, selection, q, "tg:7:checkout:2")
	if e != nil {
		t.Fatal(e)
	}
	wrong := input
	wrong.ExpectedCurrency = "USD"
	if _, e = service.CreateOrder(wrong); !errors.Is(e, app.ErrOrderCurrencyMismatch) {
		t.Fatalf("currency accepted: %v", e)
	}
	stale := input
	staleSource := *input.Source
	staleSource.ExpectedIdentityID++
	stale.Source = &staleSource
	if _, e = service.CreateOrder(stale); !errors.Is(e, delivery.ErrIdentityChanged) {
		t.Fatalf("stale generation created order: %v", e)
	}
	if e = db.Model(&order.Order{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatalf("stale generation wrote orders: %d %v", count, e)
	}
	if e = db.Model(&delivery.OrderOperation{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatalf("stale generation reserved operation: %d %v", count, e)
	}
	baseStore := ordergorm.New(db, "test-key").EnableBusinessEvents()
	raceStore := &shopBindingRaceStore{Store: baseStore, beforeWrite: func() error {
		if err := db.Delete(&identity).Error; err != nil {
			return err
		}
		identity.ID = 0
		return db.Create(&identity).Error
	}}
	raceService := app.NewOrderService(app.OrderServiceOptions{OrderStore: raceStore, UserStore: usergorm.New(db), ProductStore: productgorm.NewProductStore(db), ProductSKUStore: productgorm.NewSKUStore(db), CouponStore: coupongorm.New(db), CouponUsageStore: coupongorm.NewUsageStore(db), PromotionRepo: promotiongorm.New(db), Queue: shopQueue{}})
	if _, e = raceService.CreateOrder(input); !errors.Is(e, delivery.ErrIdentityChanged) {
		t.Fatalf("binding changed between authorization and write: %v", e)
	}
	if e = db.Model(&order.Order{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatalf("binding race wrote orders: %d %v", count, e)
	}
	input.Source.ExpectedIdentityID = identity.ID
	created, e := service.CreateOrder(input)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&sku).Updates(map[string]interface{}{"price_amount": 999, "is_active": false}).Error; e != nil {
		t.Fatal(e)
	}
	replay, e := service.CreateOrder(input)
	if e != nil {
		t.Fatal(e)
	}
	if replay.ID != created.ID {
		t.Fatal("replay duplicated order")
	}
	db.Model(&order.Order{}).Count(&count)
	if count != 2 {
		t.Fatalf("parent/child rows = %d", count)
	}
	var source delivery.OrderSource
	if e = db.Where("order_id = ?", created.ID).First(&source).Error; e != nil {
		t.Fatal(e)
	}
	if source.BotID != 7 || source.TelegramID != 123 || source.PrivateChatID != 123 {
		t.Fatalf("bad source: %+v", source)
	}
	changedSelection := input
	changedSelection.Items = append([]app.CreateOrderItem(nil), input.Items...)
	changedSelection.Items[0].Quantity++
	if _, e = service.CreateOrder(changedSelection); !errors.Is(e, delivery.ErrOperationConflict) {
		t.Fatalf("same identity changed selection replayed: %v", e)
	}
	// A revoked lifetime cannot replay an already committed order either.
	if e = db.Delete(&identity).Error; e != nil {
		t.Fatal(e)
	}
	identity.ID = 0
	if e = db.Create(&identity).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = service.CreateOrder(input); !errors.Is(e, delivery.ErrIdentityChanged) {
		t.Fatalf("stale generation replayed order: %v", e)
	}
	input.Source.ExpectedIdentityID = identity.ID
	// Same stable operation with a mutated request must never silently replay.
	input.Items[0].Quantity = 3
	if _, e = service.CreateOrder(input); !errors.Is(e, delivery.ErrOperationConflict) {
		t.Fatalf("changed request replayed: %v", e)
	}
}

func TestShopBalanceChannelHonorsProductsAndPays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:shop-wallet-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.AutoMigrate(&user.User{}, &externalidentity.Identity{}, &order.Order{}, &order.OrderItem{}, &fulfillment.Fulfillment{}, &product.Product{}, &product.ProductSKU{}, &walletdomain.Account{}, &walletdomain.Transaction{}, &paymentdomain.Payment{}, &paymentdomain.PaymentChannel{}); err != nil {
		t.Fatal(err)
	}
	u := user.User{Email: "balance@example.test", PasswordHash: "test", Status: "active"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	identity := externalidentity.Identity{UserID: u.ID, Provider: "telegram", ProviderUserID: "123"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	p := product.Product{Slug: "wallet", TitleJSON: jsonmap.JSON{"en-US": "wallet"}}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	amount := money.FromDecimal(decimal.NewFromInt(10))
	o := order.Order{UserID: u.ID, OrderNo: "WALLET", Status: constants.OrderStatusPendingPayment, Currency: "CNY", TotalAmount: amount}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order.OrderItem{OrderID: o.ID, ProductID: p.ID, Quantity: 1, TitleJSON: jsonmap.JSON{"en-US": "wallet"}}).Error; err != nil {
		t.Fatal(err)
	}
	account := walletdomain.Account{UserID: u.ID, Balance: amount}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	users := usergorm.New(db)
	products := productgorm.NewProductStore(db)
	orders := ordergorm.New(db, "test")
	walletRepo := walletgorm.New(db)
	wallet := walletapp.NewService(walletapp.Options{Repository: walletRepo, Transactions: walletRepo})
	payments := paymentapp.NewPaymentService(paymentapp.PaymentServiceOptions{OrderStore: orders, ProductRepo: products, ProductSKURepo: productgorm.NewSKUStore(db), PaymentStore: paymentgorm.New(db, "test"), ChannelStore: paymentgorm.NewChannelStore(db), WalletRepo: walletRepo, WalletService: wallet, UserStore: users})
	auth := userauth.NewService(&config.Config{}, users, identitygorm.New(db), nil, nil, nil, nil)
	auth.SetAuthUnitOfWork(userauthgorm.New(db))
	a := &shopAdapter{d: ShopDependencies{Auth: auth, Users: users, ProductRepo: products, Orders: app.NewOrderService(app.OrderServiceOptions{OrderStore: orders}), Wallet: wallet, Payments: payments}}
	actor := c.Actor{BotID: 7, TelegramID: 123, UserID: u.ID, Locale: "en-US"}
	actor.Generation, err = a.Generation(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	channels, err := a.Channels(context.Background(), actor, o.ID)
	if err != nil || len(channels) != 1 || channels[0].ID != 0 {
		t.Fatalf("full balance missing: %+v %v", channels, err)
	}
	if err := db.Model(&p).Update("payment_channel_ids", "[99]").Error; err != nil {
		t.Fatal(err)
	}
	channels, err = a.Channels(context.Background(), actor, o.ID)
	if err != nil || len(channels) != 0 {
		t.Fatalf("product restriction bypass: %+v %v", channels, err)
	}
	if _, err := a.Pay(context.Background(), actor, o.ID, 0, "restricted"); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("restricted wallet accepted: %v", err)
	}
	if err := db.Model(&p).Update("payment_channel_ids", "[]").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&account).Update("balance", 9).Error; err != nil {
		t.Fatal(err)
	}
	channels, err = a.Channels(context.Background(), actor, o.ID)
	if err != nil || len(channels) != 0 {
		t.Fatalf("insufficient wallet offered: %+v %v", channels, err)
	}
	if err := db.Model(&account).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Pay(cancelled, actor, o.ID, 0, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write accepted: %v", err)
	}
	source, err := shopTransactionSource(actor)
	if err != nil {
		t.Fatal(err)
	}
	staleSource := *source
	// Binding replacement completes after the adapter has resolved its actor.
	if err := db.Delete(&identity).Error; err != nil {
		t.Fatal(err)
	}
	identity.ID = 0
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	source.ExpectedIdentityID = identity.ID
	actor.Generation = fmt.Sprintf("%d:%d", identity.ID, u.ID)
	for _, rejected := range []struct {
		name   string
		userID uint
		source *delivery.Source
	}{
		{"old generation", u.ID, &staleSource},
		{"wrong owner", u.ID + 1, source},
		{"missing generation", u.ID, &delivery.Source{BotID: 7, TelegramID: 123, PrivateChatID: 123}},
	} {
		_, err := payments.CreatePayment(paymentapp.CreatePaymentInput{OrderID: o.ID, UseBalance: true, OperationKey: "guarded-wallet", UserID: rejected.userID, Source: rejected.source})
		if !errors.Is(err, delivery.ErrIdentityChanged) {
			t.Fatalf("%s accepted: %v", rejected.name, err)
		}
	}
	if err := db.Model(&u).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := payments.CreatePayment(paymentapp.CreatePaymentInput{OrderID: o.ID, UseBalance: true, UserID: u.ID, Source: source}); !errors.Is(err, delivery.ErrIdentityChanged) {
		t.Fatalf("disabled identity paid: %v", err)
	}
	if err := db.Model(&u).Update("status", "active").Error; err != nil {
		t.Fatal(err)
	}
	var untouched walletdomain.Account
	if err := db.First(&untouched, account.ID).Error; err != nil || !untouched.Balance.Decimal.Equal(amount.Decimal) {
		t.Fatalf("rejected guard debited wallet: %+v %v", untouched, err)
	}
	var paymentCount int64
	if err := db.Model(&paymentdomain.Payment{}).Count(&paymentCount).Error; err != nil || paymentCount != 0 {
		t.Fatalf("rejected guard persisted payment: %d %v", paymentCount, err)
	}
	result, err := a.Pay(context.Background(), actor, o.ID, 0, "full-wallet")
	if err != nil || result == nil || !result.Paid {
		t.Fatalf("wallet payment: %+v %v", result, err)
	}
	var saved walletdomain.Account
	if err := db.First(&saved, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.Balance.Decimal.IsZero() {
		t.Fatalf("wallet not charged: %s", saved.Balance.String())
	}
}
