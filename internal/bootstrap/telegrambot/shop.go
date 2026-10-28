package telegrambot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	settings "github.com/XManCodeing/XMall/internal/modules/settings/schema/messaging"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/constants"
	domaincatalog "github.com/XManCodeing/XMall/internal/modules/catalog"
	categoryapp "github.com/XManCodeing/XMall/internal/modules/catalog/category/application"
	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"
	productapp "github.com/XManCodeing/XMall/internal/modules/catalog/product/application"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	pd "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	usercontract "github.com/XManCodeing/XMall/internal/modules/identity/user/contract"
	userapp "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
	orderapp "github.com/XManCodeing/XMall/internal/modules/order/application"
	ordercontract "github.com/XManCodeing/XMall/internal/modules/order/contract"
	od "github.com/XManCodeing/XMall/internal/modules/order/domain"
	orderrisk "github.com/XManCodeing/XMall/internal/modules/orderrisk/contract"
	paymentapp "github.com/XManCodeing/XMall/internal/modules/payment/application"
	paymentdomain "github.com/XManCodeing/XMall/internal/modules/payment/domain"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	shopapp "github.com/XManCodeing/XMall/internal/modules/telegram/shop/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/shared/httpintegration"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/XManCodeing/XMall/internal/shared/money"
	"github.com/shopspring/decimal"
)

// ShopOrderWriter optionally overrides the default application-service adapter.
// Implementations must preserve atomic expected-price, operation-key and source.
type ShopOrderWriter func(context.Context, c.Actor, c.Selection, c.Quote, string) (*c.Order, error)
type ShopDependencies struct {
	Content         c.ContentProvider
	ContentSettings ContentSettings
	Store           shopapp.SessionStore
	Secret          string
	Auth            *userapp.Service
	Users           usercontract.Store
	Categories      *categoryapp.Service
	Products        *productapp.Service
	ProductRepo     productcontract.Repository
	// Cached mapping reads only: browsing must never trigger upstream HTTP.
	ProductMappings interface {
		GetByLocalProductID(uint) (*mappingdomain.Mapping, error)
	}
	SKUStocks interface {
		GetSKUMappings(uint) ([]mappingdomain.SKUMapping, error)
	}
	Orders      *orderapp.OrderService
	Payments    *paymentapp.PaymentService
	Wallet      walletcontract.UseCase
	Currency    func(string) (string, error)
	CreateOrder ShopOrderWriter
}

func NewShop(d ShopDependencies) *shopapp.Service {
	a := &shopAdapter{d: d}
	content := d.Content
	if content == nil && d.ContentSettings != nil {
		content = shopContent{settings: d.ContentSettings}
	}
	return shopapp.New(shopapp.Dependencies{Content: content, Store: d.Store, Secret: d.Secret, Identity: a, Catalog: a, Orders: a, Payments: a, Accounts: a})
}

type shopAdapter struct{ d ShopDependencies }

type ContentSettings interface {
	GetTelegramBotConfig() (settings.TelegramBotConfigSetting, error)
}

func localizedContent(values settings.LocalizedText, locale string) string {
	for _, key := range []string{locale, "zh-CN", "en-US", "zh-TW"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

// Configured content is resolved inside the shopping session for commands and callbacks.
type shopContent struct{ settings ContentSettings }

func (p shopContent) Content(_ context.Context, a c.Actor) (c.Content, error) {
	cfg, e := p.settings.GetTelegramBotConfig()
	if e != nil {
		return c.Content{}, e
	}
	out := c.Content{}
	if cfg.Welcome.Enabled {
		out.Welcome = localizedContent(cfg.Welcome.Message, a.Locale)
	}
	if cfg.Help.Enabled {
		parts := []string{}
		for _, value := range []settings.LocalizedText{cfg.Help.Title, cfg.Help.Intro} {
			if t := localizedContent(value, a.Locale); t != "" {
				parts = append(parts, t)
			}
		}
		items := append([]settings.TelegramBotHelpItem(nil), cfg.Help.Items...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		for _, item := range items {
			if item.Enabled {
				title := localizedContent(item.Title, a.Locale)
				body := localizedContent(item.Content, a.Locale)
				if body != "" {
					parts = append(parts, title+"\n"+body)
				}
			}
		}
		out.Help = strings.Join(parts, "\n\n")
	}
	if len(cfg.Menu.Items) > 0 {
		out.Menu = []c.MenuItem{}
		items := append([]settings.TelegramBotMenuItem(nil), cfg.Menu.Items...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		actions := map[string]string{"shop": "catalog", "orders": "orders", "wallet": "account", "language": "language", "help": "help"}
		for _, item := range items {
			if !item.Enabled {
				continue
			}
			label := localizedContent(item.Label, a.Locale)
			if item.Action.Type == "url" {
				out.Menu = append(out.Menu, c.MenuItem{Label: label, URL: item.Action.Value})
			} else if item.Action.Type == "builtin" {
				if kind := actions[item.Action.Value]; kind != "" {
					out.Menu = append(out.Menu, c.MenuItem{Label: label, Action: kind})
				}
			}
		}
	}
	return out, nil
}

func (a *shopAdapter) Resolve(ctx context.Context, x c.Actor, provision bool) (uint, error) {
	in := userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10), Username: x.Username}
	u, _, e := a.d.Auth.ResolveTelegramChannelIdentity(in)
	if e != nil {
		return 0, e
	}
	if u == nil && provision {
		u, _, _, e = a.d.Auth.ProvisionTelegramChannelIdentity(in)
	}
	if e != nil {
		return 0, e
	}
	if u == nil {
		return 0, c.ErrDenied
	}
	return u.ID, nil
}
func (a *shopAdapter) Generation(ctx context.Context, x c.Actor) (string, error) {
	u, identity, e := a.d.Auth.ResolveTelegramChannelIdentity(userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10)})
	if e != nil {
		return "", e
	}
	if u == nil || identity == nil || u.ID != x.UserID {
		return "", c.ErrDenied
	}
	return fmt.Sprintf("%d:%d", identity.ID, u.ID), nil
}
func (a *shopAdapter) authorize(ctx context.Context, x c.Actor) error {
	if x.BotID == 0 || x.TelegramID <= 0 || x.UserID == 0 {
		return c.ErrDenied
	}
	uid, e := a.Resolve(ctx, x, false)
	if e != nil {
		return e
	}
	if uid == 0 || uid != x.UserID {
		return c.ErrDenied
	}
	if x.Generation != "" {
		generation, e := a.Generation(ctx, x)
		if e != nil {
			return e
		}
		if generation != x.Generation {
			return c.ErrDenied
		}
	}
	return nil
}
func (a *shopAdapter) SendBindCode(ctx context.Context, x c.Actor, email string) error {
	if e := a.authorize(ctx, x); e != nil {
		return e
	}
	return shopIdentityError(a.d.Auth.SendVerifyCode(ctx, email, constants.VerifyPurposeTelegramBind, x.Locale, ""))
}
func (a *shopAdapter) Bind(ctx context.Context, x c.Actor, email, code string) (uint, error) {
	if e := a.authorize(ctx, x); e != nil {
		return 0, e
	}
	u, _, _, e := a.d.Auth.BindTelegramBotByEmailCode(x.BotID, userapp.BindTelegramChannelByEmailCodeInput{Identity: userapp.TelegramChannelIdentityInput{ChannelUserID: strconv.FormatInt(x.TelegramID, 10)}, Email: email, Code: code})
	if e != nil {
		return 0, shopIdentityError(e)
	}
	if u == nil {
		return 0, c.ErrDenied
	}
	return u.ID, nil
}
func shopIdentityError(err error) error {
	for _, target := range []error{userapp.ErrNotFound, userapp.ErrUserDisabled, userapp.ErrInvalidEmail, userapp.ErrVerifyCodeInvalid, userapp.ErrVerifyCodeExpired, userapp.ErrVerifyCodeTooFrequent, userapp.ErrVerifyCodeAttemptsExceeded, userapp.ErrUserOAuthIdentityExists, userapp.ErrUserOAuthAlreadyBound} {
		if errors.Is(err, target) {
			return c.ErrDenied
		}
	}
	return err
}
func (a *shopAdapter) HistoryUsers(ctx context.Context, x c.Actor) ([]uint, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	return a.d.Auth.AllowedTelegramHistoryUserIDs(ctx, x.BotID, strconv.FormatInt(x.TelegramID, 10), x.UserID)
}
func (a *shopAdapter) readOwner(ctx context.Context, x c.Actor, owner uint) error {
	if e := a.authorize(ctx, x); e != nil {
		return e
	}
	if x.UserID == owner {
		return nil
	}
	ok, e := a.d.Auth.CanReadTelegramHistory(ctx, x.BotID, strconv.FormatInt(x.TelegramID, 10), x.UserID, owner)
	if e != nil {
		return e
	}
	if !ok {
		return c.ErrDenied
	}
	return nil
}
func localized(j jsonmap.JSON, l string) string {
	for _, k := range []string{l, "zh-CN", "en-US", "zh-TW"} {
		if v, ok := j[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
func (a *shopAdapter) Categories(ctx context.Context, x c.Actor) ([]c.Category, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	cats, e := a.d.Categories.ListActive()
	if e != nil {
		return nil, e
	}
	out := []c.Category{}
	visible := make(map[uint]bool)
	for _, cat := range cats {
		if cat.ParentID == 0 {
			visible[cat.ID] = false
		}
	}
	for _, cat := range cats {
		if _, parentActive := visible[cat.ParentID]; cat.ParentID != 0 && parentActive {
			visible[cat.ParentID] = true
		}
	}
	for _, cat := range cats {
		if cat.ParentID != 0 || visible[cat.ID] {
			continue
		}
		_, count, err := a.d.Products.ListPublicExact(strconv.FormatUint(uint64(cat.ID), 10), 1, 1)
		if err != nil {
			return nil, err
		}
		visible[cat.ID] = count > 0
	}
	for _, cat := range cats {
		if (cat.ParentID == 0 && visible[cat.ID]) || (cat.ParentID != 0 && visible[cat.ParentID]) {
			out = append(out, c.Category{ID: cat.ID, Name: localized(cat.NameJSON, x.Locale)})
		}
	}
	return out, nil
}
func (a *shopAdapter) Products(ctx context.Context, x c.Actor, id uint, page int) ([]c.Product, bool, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, false, e
	}
	if page < 1 || page > 10000 {
		return nil, false, c.ErrDenied
	}
	items, total, e := a.d.Products.ListPublic(strconv.FormatUint(uint64(id), 10), "", page, 6)
	if e != nil {
		return nil, false, e
	}
	if e = a.d.Products.ApplyAutoStockCounts(items); e != nil {
		return nil, false, e
	}
	currency, e := a.d.Currency("CNY")
	if e != nil {
		return nil, false, e
	}
	out := []c.Product{}
	for _, p := range items {
		if p.IsActive && p.Category.IsActive {
			v := productView(p, x.Locale, &a.d)
			v.Currency = currency
			out = append(out, v)
		}
	}
	return out, int64(page*6) < total, nil
}
func productView(p pd.Product, l string, deps ...*ShopDependencies) c.Product {
	out := c.Product{ID: p.ID, Title: localized(p.TitleJSON, l), Description: localized(p.DescriptionJSON, l), Min: p.MinPurchaseQuantity, Max: p.MaxPurchaseQuantity}
	if out.Min < 1 {
		out.Min = 1
	}
	if len(out.Description) > 1500 {
		out.Description = string([]rune(out.Description)[:min(500, len([]rune(out.Description)))])
	}
	// Refuse all nonempty form schemas rather than silently bypassing required fields.
	if fields, ok := p.ManualFormSchemaJSON["fields"]; ok {
		raw, _ := json.Marshal(fields)
		out.Unsupported = string(raw) != "[]" && string(raw) != "null"
	}
	// Display only public/base prices. The selected quantity's actual payable
	// (including member/wholesale prices) is supplied by PreviewOrder, not here.
	out.Price = p.PriceAmount.String()
	stockKnown := true
	upstream := p.FulfillmentType == constants.FulfillmentTypeUpstream
	skuStocks := make(map[uint]int64)
	if upstream {
		stockKnown = false
		if len(deps) > 0 && deps[0] != nil && deps[0].ProductMappings != nil && deps[0].SKUStocks != nil {
			mapping, err := deps[0].ProductMappings.GetByLocalProductID(p.ID)
			if err == nil && mapping != nil {
				rows, err := deps[0].SKUStocks.GetSKUMappings(mapping.ID)
				if err == nil {
					stockKnown = true
					for _, row := range rows {
						if mapping.IsActive && row.UpstreamIsActive {
							skuStocks[row.LocalSKUID] = int64(row.UpstreamStock)
						}
					}
				}
			}
		}
	}
	stockText := func(quantity int64) string {
		labels := map[string]string{"unknown": "库存暂不可用", "in_stock": "有货", "low_stock": "库存紧张", "out_of_stock": "缺货", "unlimited": "不限量", "stock": "库存"}
		if l == "en-US" {
			labels = map[string]string{"unknown": "Stock unavailable", "in_stock": "In stock", "low_stock": "Low stock", "out_of_stock": "Out of stock", "unlimited": "Unlimited", "stock": "Stock"}
		} else if l == "zh-TW" {
			labels = map[string]string{"unknown": "庫存暫不可用", "in_stock": "有貨", "low_stock": "庫存緊張", "out_of_stock": "缺貨", "unlimited": "不限量", "stock": "庫存"}
		}
		if domaincatalog.NormalizeStockDisplayMode(p.StockDisplayMode) == constants.ProductStockDisplayHidden {
			return ""
		}
		if !stockKnown {
			return labels["unknown"]
		}
		policy := domaincatalog.StorefrontStockPolicy()
		display := policy.Display(p.StockDisplayMode, policy.Status(quantity), quantity)
		if display.Mode == constants.ProductStockDisplayHidden {
			return ""
		}
		if quantity <= 0 || display.Mode == constants.ProductStockDisplayStatus {
			return labels[display.Status]
		}
		if display.Mode == constants.ProductStockDisplayRange {
			if display.RangeMax == nil {
				return fmt.Sprintf("%s: %d+", labels["stock"], *display.RangeMin)
			}
			return fmt.Sprintf("%s: %d–%d", labels["stock"], *display.RangeMin, *display.RangeMax)
		}
		return fmt.Sprintf("%s: %d", labels["stock"], quantity)
	}
	totalStock := domaincatalog.StockQuantity(p.FulfillmentType, p.AutoStockAvailable, p.ManualStockTotal)
	hasSKU, unlimited := false, false
	for _, sku := range p.SKUs {
		if !sku.IsActive {
			continue
		}
		if !hasSKU {
			totalStock = 0
			hasSKU = true
		}
		quantity := domaincatalog.StockQuantity(p.FulfillmentType, sku.AutoStockAvailable, sku.ManualStockTotal)
		if upstream {
			quantity = skuStocks[sku.ID]
		}
		if quantity < 0 {
			unlimited = true
		} else {
			totalStock += quantity
		}
		name := localized(sku.SpecValuesJSON, l)
		if name == "" {
			name = sku.SKUCode
		}
		out.SKUs = append(out.SKUs, c.SKU{ID: sku.ID, Name: name, Price: sku.PriceAmount.String(), Stock: stockText(quantity)})
	}
	if upstream && !hasSKU {
		// No mapped SKU means no cached upstream inventory can be asserted.
		stockKnown = false
	}
	if unlimited {
		totalStock = -1
	}
	out.Stock = stockText(totalStock)
	return out
}
func (a *shopAdapter) Product(ctx context.Context, x c.Actor, id uint) (*c.Product, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	if id == 0 || a.d.Products == nil {
		return nil, c.ErrDenied
	}
	// Resolve the callback ID through the read application service, then use
	// its public query so visibility and active-SKU rules stay authoritative.
	p, e := a.d.Products.GetAdminByID(strconv.FormatUint(uint64(id), 10))
	if errors.Is(e, productcontract.ErrNotFound) {
		return nil, c.ErrDenied
	}
	if e != nil {
		return nil, e
	}
	if p == nil || !p.IsActive || !p.Category.IsActive || p.DeletedAt != nil {
		return nil, c.ErrDenied
	}
	p, e = a.d.Products.GetPublicBySlug(p.Slug)
	if errors.Is(e, productcontract.ErrNotFound) {
		return nil, c.ErrDenied
	}
	if e != nil {
		return nil, e
	}
	if p == nil || p.ID != id || !p.IsActive || !p.Category.IsActive {
		return nil, c.ErrDenied
	}
	items := []pd.Product{*p}
	if e = a.d.Products.ApplyAutoStockCounts(items); e != nil {
		return nil, e
	}
	currency, e := a.d.Currency("CNY")
	if e != nil {
		return nil, e
	}
	v := productView(items[0], x.Locale, &a.d)
	v.Currency = currency
	return &v, nil
}
func orderInput(x c.Actor, s c.Selection) orderapp.CreateOrderInput {
	return orderapp.CreateOrderInput{UserID: x.UserID, Items: []orderapp.CreateOrderItem{{ProductID: s.ProductID, SKUID: s.SKUID, Quantity: s.Quantity}}, SkipIPRiskControl: true}
}
func (a *shopAdapter) Preview(ctx context.Context, x c.Actor, s c.Selection) (*c.Quote, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	p, e := a.Product(ctx, x, s.ProductID)
	if e != nil {
		return nil, e
	}
	if p.Unsupported || s.Quantity < p.Min || (p.Max > 0 && s.Quantity > p.Max) {
		return nil, c.ErrDenied
	}
	q, e := a.d.Orders.PreviewOrder(orderInput(x, s))
	if e != nil {
		return nil, shopOrderError(e)
	}
	if q == nil {
		return nil, c.ErrUnavailable
	}
	raw, e := json.Marshal(q)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(raw)
	return &c.Quote{Amount: q.TotalAmount.String(), Currency: q.Currency, Fingerprint: hex.EncodeToString(h[:])}, nil
}
func shopOrderError(err error) error {
	if errors.Is(err, delivery.ErrIdentityChanged) {
		return c.ErrDenied
	}
	for _, target := range []error{orderapp.ErrInvalidOrderItem, orderapp.ErrInvalidOrderAmount, orderapp.ErrProductNotAvailable, orderapp.ErrProductPurchaseNotAllowed, orderapp.ErrProductMaxPurchaseExceeded, orderapp.ErrProductMinPurchaseNotMet, orderapp.ErrProductSKURequired, orderapp.ErrProductSKUInvalid, orderapp.ErrCardSecretInsufficient, orderapp.ErrManualStockInsufficient, orderrisk.ErrTooManyPendingOrders, orderrisk.ErrProductQuantityLimit, orderrisk.ErrPendingProductQuantityLimit, orderrisk.ErrOrderRateLimited} {
		if errors.Is(err, target) {
			return c.ErrDenied
		}
	}
	return err
}
func (a *shopAdapter) Create(ctx context.Context, x c.Actor, s c.Selection, q c.Quote, key string) (*c.Order, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	if q.Fingerprint == "" || key == "" || x.Generation == "" || x.BotID == 0 || x.TelegramID <= 0 {
		return nil, c.ErrDenied
	}
	if a.d.CreateOrder != nil {
		out, err := a.d.CreateOrder(ctx, x, s, q, key)
		if errors.Is(err, orderapp.ErrInvalidOrderAmount) || errors.Is(err, orderapp.ErrOrderCurrencyMismatch) {
			return nil, c.ErrPriceChanged
		}
		if err != nil {
			return nil, shopOrderError(err)
		}
		if out == nil || out.UserID != x.UserID {
			return nil, c.ErrDenied
		}
		return out, nil
	}
	input, err := shopCreateInput(x, s, q, key)
	if err != nil {
		return nil, err
	}
	out, err := a.d.Orders.CreateOrder(input)
	if errors.Is(err, orderapp.ErrInvalidOrderAmount) || errors.Is(err, orderapp.ErrOrderCurrencyMismatch) {
		return nil, c.ErrPriceChanged
	}
	if err != nil {
		return nil, shopOrderError(err)
	}
	if out == nil || out.UserID != x.UserID {
		return nil, c.ErrDenied
	}
	v := orderView(out)
	return &v, nil
}
func shopTransactionSource(x c.Actor) (*delivery.Source, error) {
	parts := strings.Split(x.Generation, ":")
	if len(parts) != 2 || x.UserID == 0 {
		return nil, c.ErrDenied
	}
	identityID, err := strconv.ParseUint(parts[0], 10, strconv.IntSize)
	if err != nil || identityID == 0 || parts[1] != strconv.FormatUint(uint64(x.UserID), 10) {
		return nil, c.ErrDenied
	}
	source := &delivery.Source{BotID: x.BotID, TelegramID: x.TelegramID, PrivateChatID: x.TelegramID, ExpectedIdentityID: uint(identityID)}
	if source.Validate() != nil {
		return nil, c.ErrDenied
	}
	return source, nil
}

func shopCreateInput(x c.Actor, s c.Selection, q c.Quote, key string) (orderapp.CreateOrderInput, error) {
	source, err := shopTransactionSource(x)
	if err != nil {
		return orderapp.CreateOrderInput{}, err
	}
	amount, err := decimal.NewFromString(q.Amount)
	if err != nil || amount.IsNegative() || q.Currency == "" {
		return orderapp.CreateOrderInput{}, c.ErrDenied
	}
	expected := money.FromDecimal(amount)
	input := orderInput(x, s)
	input.OperationKey = key
	request, err := json.Marshal(struct {
		Quote      c.Quote
		Selection  c.Selection
		Generation string
	}{q, s, x.Generation})
	if err != nil {
		return orderapp.CreateOrderInput{}, err
	}
	digest := sha256.Sum256(request)
	input.RequestHash = hex.EncodeToString(digest[:])
	input.Source = source
	input.ExpectedTotalAmount = &expected
	input.ExpectedCurrency = q.Currency
	return input, nil
}
func orderView(o *od.Order) c.Order {
	v := c.Order{ID: o.ID, UserID: o.UserID, Number: o.OrderNo, Status: o.Status, Amount: o.TotalAmount.String(), Currency: o.Currency, ExpiresAt: o.ExpiresAt, Pending: o.ParentID == nil && o.Status == constants.OrderStatusPendingPayment && (o.ExpiresAt == nil || o.ExpiresAt.After(time.Now()))}
	if o.Fulfillment != nil && o.Fulfillment.Status == "delivered" && o.Fulfillment.Payload != "" {
		v.Delivered = append(v.Delivered, o.Fulfillment.Payload)
	}
	for i := range o.Children {
		child := orderView(&o.Children[i])
		v.Delivered = append(v.Delivered, child.Delivered...)
	}
	return v
}
func (a *shopAdapter) List(ctx context.Context, x c.Actor, owner uint, page int) ([]c.Order, bool, error) {
	if e := a.readOwner(ctx, x, owner); e != nil {
		return nil, false, e
	}
	if page < 1 || page > 10000 {
		return nil, false, c.ErrDenied
	}
	items, total, e := a.d.Orders.ListOrdersByUser(ordercontract.ListFilter{UserID: owner, Page: page, PageSize: 6})
	if e != nil {
		return nil, false, e
	}
	out := []c.Order{}
	for i := range items {
		out = append(out, orderView(&items[i]))
	}
	return out, int64(page*6) < total, nil
}
func (a *shopAdapter) Detail(ctx context.Context, x c.Actor, id, owner uint) (*c.Order, error) {
	if e := a.readOwner(ctx, x, owner); e != nil {
		return nil, e
	}
	o, e := a.d.Orders.GetOrderByUser(id, owner)
	if e != nil {
		return nil, e
	}
	if o == nil {
		return nil, c.ErrDenied
	}
	v := orderView(o)
	return &v, nil
}
func (a *shopAdapter) Account(ctx context.Context, x c.Actor) (*c.Account, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	u, e := a.d.Users.GetByID(x.UserID)
	if e != nil {
		return nil, e
	}
	if u == nil {
		return nil, c.ErrDenied
	}
	balances, e := a.d.Wallet.GetBalancesByUserIDs([]uint{x.UserID})
	if e != nil {
		return nil, e
	}
	currency, e := a.d.Currency("CNY")
	if e != nil {
		return nil, e
	}
	balance := "0.00"
	if w, ok := balances[x.UserID]; ok {
		balance = w.String()
	}
	return &c.Account{ID: u.ID, Email: u.Email, Balance: balance, Currency: currency}, nil
}
func (a *shopAdapter) Channels(ctx context.Context, x c.Actor, id uint) ([]c.Channel, error) {
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	o, e := a.d.Orders.GetOrderByUser(id, x.UserID)
	if e != nil {
		return nil, e
	}
	if o == nil || !orderView(o).Pending {
		return nil, c.ErrDenied
	}
	u, e := a.d.Users.GetByID(x.UserID)
	if e != nil {
		return nil, e
	}
	if u == nil {
		return nil, c.ErrDenied
	}
	eligible, e := a.d.Payments.GetAvailableChannels(paymentapp.AvailablePaymentChannelFilter{TargetAmount: &o.TotalAmount, User: u, PaymentType: constants.PaymentTypeOrder})
	if e != nil {
		return nil, e
	}
	ids := []uint{}
	items := append([]od.OrderItem{}, o.Items...)
	for _, child := range o.Children {
		items = append(items, child.Items...)
	}
	for _, item := range items {
		ids = append(ids, item.ProductID)
	}
	if len(ids) == 0 {
		return nil, c.ErrDenied
	}
	allowed, e := a.d.Payments.GetAllowedChannelsForProducts(ids)
	if e != nil {
		return nil, e
	}
	set := map[uint]bool{}
	for _, ch := range eligible {
		if id, ok := ch["id"].(uint); ok {
			set[id] = true
		}
	}
	out := []c.Channel{}
	balances, e := a.d.Wallet.GetBalancesByUserIDs([]uint{x.UserID})
	if e != nil {
		return nil, e
	}
	availableBalance := balances[x.UserID].Decimal.Add(o.WalletPaidAmount.Decimal)
	canCover := availableBalance.GreaterThanOrEqual(o.TotalAmount.Decimal)
	for _, ch := range allowed {
		wallet := ch.ProviderType == constants.PaymentProviderWallet || ch.ChannelType == constants.PaymentChannelTypeBalance
		if set[ch.ID] && ch.IsActive && (!wallet || canCover) {
			out = append(out, c.Channel{ID: ch.ID, Name: ch.Name})
		}
	}
	// Channel zero is the shared payment service's full-wallet route. Never
	// use it to bypass an explicit product channel allow-list. Read products
	// directly because GetAllowedChannelIDsForOrder fails open on store errors.
	if canCover {
		products, err := a.d.ProductRepo.ListByIDs(ids)
		if err != nil {
			return nil, err
		}
		seen := map[uint]bool{}
		unrestricted := true
		for _, product := range products {
			seen[product.ID] = true
			if len(pd.DecodePaymentChannelIDs(product.PaymentChannelIDs)) != 0 {
				unrestricted = false
			}
		}
		for _, productID := range ids {
			if !seen[productID] {
				unrestricted = false
			}
		}
		if unrestricted {
			name := "余额支付"
			if x.Locale == "en-US" {
				name = "Pay with balance"
			} else if x.Locale == "zh-TW" {
				name = "餘額支付"
			}
			out = append(out, c.Channel{ID: 0, Name: name})
		}
	}
	return out, nil
}
func (a *shopAdapter) Pay(ctx context.Context, x c.Actor, id, channel uint, key string) (*c.Payment, error) {
	if x.Generation == "" || key == "" {
		return nil, c.ErrDenied
	}
	channels, e := a.Channels(ctx, x, id)
	if e != nil {
		return nil, e
	}
	ok := false
	for _, ch := range channels {
		if ch.ID == channel {
			ok = true
		}
	}
	if !ok {
		return nil, c.ErrDenied
	}
	useBalance := channel == 0
	qrKind := "text"
	if channel != 0 {
		ch, err := a.d.Payments.GetChannel(channel)
		if err != nil {
			return nil, err
		}
		if ch == nil {
			return nil, c.ErrDenied
		}
		useBalance = ch.ProviderType == constants.PaymentProviderWallet || ch.ChannelType == constants.PaymentChannelTypeBalance
		// Explicit channel contract for ambiguous gateway qrcode fields. Never
		// infer image semantics by fetching an otherwise valid payment URL.
		if kind, _ := ch.ConfigJSON["telegram_qr_code_kind"].(string); kind != "" {
			if kind != "text" && kind != "image" {
				return nil, c.ErrUnavailable
			}
			qrKind = kind
		}
	}
	order, e := a.d.Orders.GetOrderByUser(id, x.UserID)
	if e != nil {
		return nil, e
	}
	if order == nil {
		return nil, c.ErrDenied
	}
	// Channel discovery performs several reads. Recheck the identity immediately
	// before writing and retain the caller's context (including mutation guards).
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := a.authorize(ctx, x); e != nil {
		return nil, e
	}
	source, e := shopTransactionSource(x)
	if e != nil {
		return nil, e
	}
	result, e := a.d.Payments.CreatePayment(paymentapp.CreatePaymentInput{OperationKey: key, OrderID: id, ChannelID: channel, UseBalance: useBalance, Context: ctx, UserID: x.UserID, Source: source})
	if errors.Is(e, delivery.ErrIdentityChanged) {
		return nil, c.ErrDenied
	}
	if errors.Is(e, paymentapp.ErrPaymentCreationUncertain) {
		return nil, c.ErrPaymentUncertain
	}
	if e != nil {
		return nil, e
	}
	if result == nil {
		return nil, c.ErrUnavailable
	}
	if result.OrderPaid {
		return &c.Payment{Paid: true, OrderNumber: order.OrderNo}, nil
	}
	if result.Payment == nil {
		return nil, c.ErrUnavailable
	}
	p := result.Payment
	// Gateways which explicitly return qrcode_type can declare image media
	// without a channel override; untyped qrcode is always scan content.
	if kind, _ := p.ProviderPayload["qrcode_type"].(string); kind == "image" || kind == "text" {
		qrKind = kind
	}
	// PayURL is already the gateway application result, including the signed local
	// epay POST bridge URL. Never reconstruct a GET URL from provider parameters.
	if strings.HasPrefix(strings.TrimSpace(p.PayURL), "{") {
		return nil, fmt.Errorf("%w: malformed payment URL", c.ErrUnavailable)
	}
	out := shopPayment(p, order, qrKind)
	return resolvePaymentMedia(ctx, out, httpintegration.FetchPaymentImage)
}

func shopPayment(p *paymentdomain.Payment, order *od.Order, qrKind string) *c.Payment {
	return &c.Payment{Amount: p.Amount.String(), Currency: p.Currency, PayURL: p.PayURL, QRCode: p.QRCode, QRCodeKind: qrKind, OrderNumber: order.OrderNo, InteractionMode: p.InteractionMode, ExpiresAt: order.ExpiresAt}
}

func resolvePaymentMedia(ctx context.Context, out *c.Payment, fetch func(context.Context, string) ([]byte, error)) (*c.Payment, error) {
	p := *out
	if p.InteractionMode != constants.PaymentInteractionRedirect && p.QRCodeKind == "image" {
		raw := strings.TrimSpace(p.QRCode)
		if raw == "" && p.InteractionMode == constants.PaymentInteractionQR {
			raw = strings.TrimSpace(p.PayURL)
		}
		if parsed, err := url.Parse(raw); err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") {
			photo, e := fetch(ctx, raw)
			out.QRImage = photo
			if e != nil || len(photo) == 0 {
				// A separate checkout remains usable. Never turn an unverified
				// image endpoint into QR text, or offer that same endpoint as checkout.
				if strings.TrimSpace(p.PayURL) == "" || strings.TrimSpace(p.PayURL) == raw {
					return nil, c.ErrUnavailable
				}
				out.QRCode = ""
				out.InteractionMode = constants.PaymentInteractionRedirect
			}
			if len(out.QRImage) > 0 && strings.TrimSpace(out.PayURL) == raw {
				out.PayURL = ""
			}
		}
	}
	return out, nil
}

// HistoryAccount is a pure read; even a missing wallet must not create an account.
func (a *shopAdapter) HistoryAccount(ctx context.Context, x c.Actor, owner uint) (*c.Account, error) {
	if err := a.authorizeHistoryWallet(ctx, x, owner); err != nil {
		return nil, err
	}
	balances, err := a.d.Wallet.GetBalancesByUserIDs([]uint{owner})
	if err != nil {
		return nil, err
	}
	currency, err := a.d.Currency("CNY")
	if err != nil {
		return nil, err
	}
	balance := "0.00"
	if amount, ok := balances[owner]; ok {
		balance = amount.String()
	}
	// Do not disclose the previous account's email/profile via a wallet grant.
	return &c.Account{ID: owner, Balance: balance, Currency: currency}, nil
}

func (a *shopAdapter) HistoryRefunds(ctx context.Context, x c.Actor, owner uint, page int) ([]c.Refund, bool, error) {
	if err := a.authorizeHistoryWallet(ctx, x, owner); err != nil {
		return nil, false, err
	}
	if page < 1 || page > 10000 {
		return nil, false, c.ErrDenied
	}
	rows, total, err := a.d.Wallet.ListTransactions(walletcontract.TransactionListFilter{UserID: owner, Type: constants.WalletTxnTypeOrderRefund, Page: page, PageSize: 6})
	if err != nil {
		return nil, false, err
	}
	out := make([]c.Refund, 0, len(rows))
	for _, row := range rows {
		if row.UserID != owner || row.Type != constants.WalletTxnTypeOrderRefund {
			return nil, false, c.ErrDenied
		}
		out = append(out, c.Refund{Amount: row.Amount.String(), Currency: row.Currency, Reference: row.Reference, CreatedAt: row.CreatedAt})
	}
	return out, int64(page*6) < total, nil
}

func (a *shopAdapter) authorizeHistoryWallet(ctx context.Context, x c.Actor, owner uint) error {
	if err := a.authorize(ctx, x); err != nil {
		return err
	}
	if owner == 0 || owner == x.UserID {
		return c.ErrDenied
	}
	owners, err := a.HistoryUsers(ctx, x)
	if err != nil {
		return err
	}
	for _, allowed := range owners {
		if allowed == owner {
			return nil
		}
	}
	return c.ErrDenied
}
