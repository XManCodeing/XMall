package telegrambot

import (
	"context"
	"errors"
	"github.com/XManCodeing/XMall/internal/constants"
	productcontract "github.com/XManCodeing/XMall/internal/modules/catalog/product/contract"
	product "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	fulfillment "github.com/XManCodeing/XMall/internal/modules/fulfillment/domain"
	order "github.com/XManCodeing/XMall/internal/modules/order/domain"
	paymentcontract "github.com/XManCodeing/XMall/internal/modules/payment/contract"
	settingsapp "github.com/XManCodeing/XMall/internal/modules/settings/application"
	delivery "github.com/XManCodeing/XMall/internal/modules/telegram/delivery/contract"
	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	memberdomain "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestShopPaymentErrorFeedback verifies direct/wrapped wallet failures reuse safe shop feedback.
// Uncertain outcomes must retain their blocking classification; unrelated failures remain diagnostic.
func TestShopPaymentErrorFeedback(t *testing.T) {
	unexpected := errors.New("payment storage unavailable")
	for _, test := range []struct {
		name        string
		input, want error
	}{
		{"success", nil, nil},
		{"wallet-only", walletcontract.ErrOnlyPaymentRequired, c.ErrWalletOnlyPaymentRequired},
		{"balance", walletcontract.ErrInsufficientBalance, c.ErrInsufficientBalance},
		{"wrapped balance", errors.Join(errors.New("wallet debit rejected"), walletcontract.ErrInsufficientBalance), c.ErrInsufficientBalance},
		{"identity", delivery.ErrIdentityChanged, c.ErrDenied},
		{"uncertain", errors.Join(paymentcontract.ErrPaymentCreationUncertain, walletcontract.ErrInsufficientBalance), c.ErrPaymentUncertain},
		{"unexpected", unexpected, unexpected},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shopPaymentError(test.input); !errors.Is(got, test.want) {
				t.Fatalf("mapped error = %v, want %v", got, test.want)
			}
		})
	}
	var retry *c.PaymentRetryError
	if got := shopPaymentError(&paymentcontract.PaymentAttemptRetryError{OperationKey: "authorized-successor"}); !errors.As(got, &retry) || retry.OperationKey != "authorized-successor" {
		t.Fatalf("retry classification lost: %v", got)
	}
}

// Update dispatcher composition regressions preserve the shop boundary.
type dispatchShop struct {
	updates            []rt.Update
	deliveries, stamps int
	stamp              string
	err                error
}

func (s *dispatchShop) Handle(_ context.Context, _ uint, u rt.Update) ([]rt.Message, error) {
	s.updates = append(s.updates, u)
	return nil, nil
}
func (s *dispatchShop) AuthorizeDelivery(context.Context, uint, int64, uint) error {
	s.deliveries++
	return s.err
}
func (s *dispatchShop) AuthorizeStamp(_ context.Context, _ uint, _ int64, stamp string) error {
	s.stamps++
	s.stamp = stamp
	return s.err
}

type dispatchLegacy struct {
	handled bool
	err     error
	calls   int
}

func (s *dispatchLegacy) ConsumeLegacyInput(context.Context, uint, rt.Update) ([]rt.Message, bool, error) {
	s.calls++
	return nil, s.handled, s.err
}

type dispatchModule struct {
	updates []rt.Update
	enabled bool
	checks  int
	err     error
}

func (m *dispatchModule) Handle(_ context.Context, _ uint, u rt.Update) ([]rt.Message, error) {
	m.updates = append(m.updates, u)
	return nil, m.err
}
func (m *dispatchModule) ReadyEnabled(context.Context, uint) (bool, error) {
	m.checks++
	return m.enabled, m.err
}

type dispatchSubscription []string

func (s dispatchSubscription) AllowedUpdates(context.Context, uint) ([]string, error) {
	return []string(s), nil
}

type dispatchTarget struct{ got rt.Message }

func (a *dispatchTarget) AuthorizeTarget(_ context.Context, _ uint, m rt.Message) error {
	a.got = m
	return nil
}

type membershipReaderStub struct {
	config *memberdomain.ProductConfig
	err    error
	ids    []uint
}

// Display records the actual product ID and returns the persisted extension or error.
func (reader *membershipReaderStub) Display(_ context.Context, productID uint) (*membership.DisplayConfig, error) {
	reader.ids = append(reader.ids, productID)
	if reader.config == nil {
		return nil, reader.err
	}
	return &membership.DisplayConfig{SalesOpen: reader.config.Enabled, ProductID: reader.config.ProductID, Version: reader.config.Version, Quantity: reader.config.Quantity, Terms: reader.config.Terms, GroupNames: []string{"Group two", "<b>Group one</b>"}}, reader.err
}

type shopSalePolicy struct {
	productcontract.Repository
	err error
	ids []uint
}

// CheckOrdinaryWrite records product IDs through the shared policy stub.
func (policy *shopSalePolicy) CheckOrdinaryWrite(ids []uint) error {
	return policy.CheckPublicSale(ids)
}

// CheckPublicSale records product IDs and returns the configured outcome.
func (policy *shopSalePolicy) CheckPublicSale(ids []uint) error {
	policy.ids = append(policy.ids, ids...)
	return policy.err
}

// TestShopMembershipProjection exercises custom months, permanent and DEFAULT configuration.
func TestShopMembershipProjection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		config := &memberdomain.ProductConfig{ProductID: 2, Quantity: 1, Enabled: enabled}
		source := product.Product{ID: 2, PurchaseType: "guest", MinPurchaseQuantity: 8}
		for index, months := range []int64{2, 5, 18, 0} {
			kind := "months"
			if months == 0 {
				kind = "permanent"
			}
			skuID := uint(index + 10)
			config.Terms = append(config.Terms, memberdomain.SKUTerm{ProductID: 2, SKUID: skuID, Kind: kind, Months: months})
			source.SKUs = append(source.SKUs, product.ProductSKU{ID: skuID, ProductID: 2, SKUCode: "DEFAULT", IsActive: true})
		}
		source.SKUs = append(source.SKUs, product.ProductSKU{ID: 20, ProductID: 2, IsActive: false})
		config.Terms = append(config.Terms, memberdomain.SKUTerm{ProductID: 2, SKUID: 20, Kind: "months", Months: 3})
		reader := &membershipReaderStub{config: config}
		adapter := &shopAdapter{d: ShopDependencies{Membership: reader}}
		view := productView(source, "en-US")
		view.Available = true
		for index := range view.SKUs {
			view.SKUs[index].Available = true
		}
		if err := adapter.membershipProduct(context.Background(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.Membership || view.Available != enabled || view.Min != 1 || view.Max != 1 || len(view.SKUs) != 4 || len(reader.ids) != 1 || reader.ids[0] != 2 {
			t.Fatalf("bad projection: %+v", view)
		}
		if !reflect.DeepEqual(view.GroupNames, []string{"Group two", "<b>Group one</b>"}) {
			t.Fatalf("group names/order: %+v", view.GroupNames)
		}
		for index, sku := range view.SKUs {
			if sku.Available != enabled || sku.MembershipTerm == nil || sku.MembershipTerm.Months != config.Terms[index].Months || sku.MembershipTerm.Kind != config.Terms[index].Kind {
				t.Fatalf("wrong term: %+v", sku)
			}
		}
	}
}

// TestShopMembershipOrdinaryAndFailClosed keeps login-only products ordinary without downgrading errors.
func TestShopMembershipOrdinaryAndFailClosed(t *testing.T) {
	source := product.Product{ID: 2, PurchaseType: "member", MinPurchaseQuantity: 3, MaxPurchaseQuantity: 10}
	for _, test := range []struct {
		name        string
		reader      *membershipReaderStub
		policy      *shopSalePolicy
		unavailable bool
	}{
		{name: "ordinary login", reader: &membershipReaderStub{err: membership.ErrNotFound}},
		{name: "reader error", reader: &membershipReaderStub{err: errors.New("private storage detail")}, unavailable: true},
		{name: "nil config", reader: &membershipReaderStub{}, unavailable: true},
		{name: "missing reader and policy", unavailable: true},
		{name: "missing reader closed policy", policy: &shopSalePolicy{err: membership.ErrSalesClosed}, unavailable: true},
		{name: "legacy ordinary policy", policy: &shopSalePolicy{}},
		{name: "wrong product", reader: &membershipReaderStub{config: &memberdomain.ProductConfig{ProductID: 3, Quantity: 1}}, unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := ShopDependencies{}
			if test.reader != nil {
				deps.Membership = test.reader
			}
			if test.policy != nil {
				deps.ProductRepo = test.policy
			}
			view := productView(source, "en-US")
			err := (&shopAdapter{d: deps}).membershipProduct(context.Background(), &view)
			if test.unavailable {
				if !errors.Is(err, c.ErrUnavailable) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || view.Membership || view.Min != 3 || view.Max != 10 {
				t.Fatalf("ordinary changed: %+v %v", view, err)
			}
		})
	}
}

// TestShopSaleGuardCannotBeSkipped rejects missing shared policy and closed sales.
func TestShopSaleGuardCannotBeSkipped(t *testing.T) {
	policy := &shopSalePolicy{err: membership.ErrSalesClosed}
	adapter := &shopAdapter{d: ShopDependencies{ProductRepo: policy}}
	if err := adapter.checkShopPublicSale(2); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	if len(policy.ids) != 1 || policy.ids[0] != 2 {
		t.Fatal(policy.ids)
	}
	adapter.d.ProductRepo = nil
	if err := adapter.checkShopPublicSale(2); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
}

// TestShopMembershipInvalidTerms rejects malformed terms instead of exposing normal checkout.
func TestShopMembershipInvalidTerms(t *testing.T) {
	for _, term := range []memberdomain.SKUTerm{
		{ProductID: 2, SKUID: 10, Kind: "months", Months: 0},
		{ProductID: 2, SKUID: 10, Kind: "months", Months: -2},
		{ProductID: 2, SKUID: 10, Kind: "months", Months: 999999999},
		{ProductID: 2, SKUID: 10, Kind: "permanent", Months: 1},
		{ProductID: 2, SKUID: 10, Kind: "years", Months: 2},
		{ProductID: 3, SKUID: 10, Kind: "months", Months: 2},
	} {
		reader := &membershipReaderStub{config: &memberdomain.ProductConfig{ProductID: 2, Quantity: 1, Terms: []memberdomain.SKUTerm{term}}}
		view := c.Product{ID: 2, SKUs: []c.SKU{{ID: 10}}}
		if err := (&shopAdapter{d: ShopDependencies{Membership: reader}}).membershipProduct(context.Background(), &view); !errors.Is(err, c.ErrUnavailable) {
			t.Fatalf("invalid term accepted: %+v %v", term, err)
		}
	}
}

func TestUpdateDispatcherRouting(t *testing.T) {
	ctx := context.Background()
	shop, legacy, support := &dispatchShop{}, &dispatchLegacy{}, &dispatchModule{enabled: true}
	d := NewUpdateDispatcher(shop, legacy, UpdateModules{Support: support})
	ordinary := rt.Update{ID: 1, UserID: 11, ChatID: 11, Private: true, Text: "question"}
	if _, err := d.Handle(ctx, 1, ordinary); err != nil {
		t.Fatal(err)
	}
	if len(shop.updates) != 0 || len(support.updates) != 1 {
		t.Fatal("consultation reached shop")
	}
	legacy.handled = true
	if _, err := d.Handle(ctx, 1, ordinary); err != nil {
		t.Fatal(err)
	}
	if len(support.updates) != 1 {
		t.Fatal("legacy input leaked")
	}
	legacy.handled = false
	legacy.err = errors.New("classification unavailable")
	if _, err := d.Handle(ctx, 1, ordinary); err == nil {
		t.Fatal("classification failure swallowed")
	}
	if len(support.updates) != 1 {
		t.Fatal("failed classification leaked")
	}
	legacy.err = nil
	support.enabled = false
	if _, err := d.Handle(ctx, 1, ordinary); err != nil {
		t.Fatal(err)
	}
	if len(shop.updates) != 1 {
		t.Fatal("disabled support lost shopping fallback")
	}
}

func TestUpdateDispatcherRejectsUnknownAndMalformedCallbacks(t *testing.T) {
	shop, legacy := &dispatchShop{}, &dispatchLegacy{}
	support, member := &dispatchModule{}, &dispatchModule{}
	d := NewUpdateDispatcher(shop, legacy, UpdateModules{SupportCallbacks: support, MemberCallbacks: member})
	base := rt.Update{ID: 1, UserID: 11, ChatID: 11, Private: true, CallbackID: "callback"}
	for _, data := range []string{"unknown:x", "s:" + strings.Repeat("x", 63), "support:" + strings.Repeat("中", 20), ""} {
		u := base
		u.CallbackData = data
		if _, err := d.Handle(context.Background(), 1, u); err != nil {
			t.Fatal(err)
		}
	}
	if len(shop.updates)+len(support.updates)+len(member.updates) != 0 {
		t.Fatal("invalid callback dispatched")
	}
	for _, data := range []string{"s:original:1:a", "support:short", "member:short"} {
		u := base
		u.CallbackData = data
		if _, err := d.Handle(context.Background(), 1, u); err != nil {
			t.Fatal(err)
		}
	}
	if len(shop.updates) != 1 || shop.updates[0].CallbackData != "s:original:1:a" || len(support.updates) != 1 || len(member.updates) != 1 {
		t.Fatal("callback namespaces not preserved")
	}
	if legacy.calls != 0 {
		t.Fatal("callback consulted sensitive session")
	}
}

func TestUpdateDispatcherPrivateEnvelopeAndEvents(t *testing.T) {
	shop, legacy := &dispatchShop{}, &dispatchLegacy{}
	d := NewUpdateDispatcher(shop, legacy, UpdateModules{})
	for _, u := range []rt.Update{
		{Private: true, ChatID: -11, UserID: -11},
		{Private: true, ChatID: 11, UserID: 12},
		{Private: true, ChatID: 11, UserID: 11, ChatType: "group"},
		{Private: true, ChatID: 11, UserID: 11, Type: "unknown"},
		{Private: true, ChatID: 11, UserID: 11, Type: "chat_member"},
		{Type: "message", ChatID: -11, ChatType: "supergroup"},
		{Type: "message", Private: true, ChatID: 11, UserID: 11, SenderChat: &rt.ChatReference{ID: -1}},
	} {
		if _, err := d.Handle(context.Background(), 1, u); err != nil {
			t.Fatal(err)
		}
	}
	if len(shop.updates) != 0 || legacy.calls != 0 {
		t.Fatal("nonprivate event touched shop")
	}
	events := &dispatchModule{}
	d = NewUpdateDispatcher(shop, legacy, UpdateModules{Events: map[string]UpdateReceiver{"chat_member": events, "migration": events}})
	for _, u := range []rt.Update{{Type: "chat_member", ChatID: -1}, {Type: "message", ChatID: -1, Migration: &rt.ChatMigration{FromChatID: -1, ToChatID: -2}}} {
		if _, err := d.Handle(context.Background(), 1, u); err != nil {
			t.Fatal(err)
		}
	}
	if len(events.updates) != 2 || len(shop.updates) != 0 || legacy.calls != 0 {
		t.Fatal("events not independent")
	}
}

// TestTopicServiceNeverFallsThrough proves native lifecycle events are not replies.
func TestTopicServiceNeverFallsThrough(t *testing.T) {
	shop, group, topics := &dispatchShop{}, &dispatchModule{}, &dispatchModule{}
	update := rt.Update{Type: "message", ChatType: "supergroup", ChatID: -100, ThreadID: 5, MessageKind: "service", Text: "/support"}
	for _, registered := range []bool{false, true} {
		modules := UpdateModules{GroupMessages: group}
		if registered {
			modules.Events = map[string]UpdateReceiver{"topic_service": topics}
		}
		dispatcher := NewUpdateDispatcher(shop, &dispatchLegacy{}, modules)
		if _, err := dispatcher.Handle(context.Background(), 1, update); err != nil {
			t.Fatal(err)
		}
	}
	if len(topics.updates) != 1 || len(group.updates) != 0 || len(shop.updates) != 0 {
		t.Fatal("topic service leaked to conversational routing")
	}
}

func TestUpdateDispatcherAuthorizationAndSubscription(t *testing.T) {
	ctx := context.Background()
	shop := &dispatchShop{}
	d := NewUpdateDispatcher(shop, &dispatchLegacy{}, UpdateModules{})
	m := rt.Message{ChatID: 11, OwnerUserID: 7, IdentityStamp: "generation"}
	if err := d.AuthorizeTarget(ctx, 1, m); err != nil {
		t.Fatal(err)
	}
	if shop.deliveries != 1 || shop.stamps != 1 || shop.stamp != "generation" {
		t.Fatal("wrapper lost stamp capability")
	}
	for _, typ := range []rt.TargetType{rt.TargetSupportPrivate, rt.TargetSupportGroup, rt.TargetMemberOperation, "unknown"} {
		m.TargetType = typ
		if err := d.AuthorizeTarget(ctx, 1, m); err == nil {
			t.Fatalf("unregistered target %s authorized", typ)
		}
	}
	for _, m := range []rt.Message{{ChatID: -11, OwnerUserID: 7}, {ChatID: 11}, {ChatID: 11, OwnerUserID: 7, ThreadID: 4}} {
		if err := d.AuthorizeTarget(ctx, 1, m); err == nil {
			t.Fatalf("invalid legacy destination authorized: %+v", m)
		}
	}
	target := &dispatchTarget{}
	d = NewUpdateDispatcher(shop, &dispatchLegacy{}, UpdateModules{
		Targets:       map[rt.TargetType]rt.TargetAuthorizer{rt.TargetSupportGroup: target},
		Subscriptions: []UpdateSubscriptions{dispatchSubscription{"chat_member", "message"}, dispatchSubscription{"chat_join_request", "chat_member"}},
	})
	m = rt.Message{TargetType: rt.TargetSupportGroup, ChatID: -2, ThreadID: 4, BusinessRef: "trusted-ref", RouteVersion: 3}
	if err := d.AuthorizeTarget(ctx, 1, m); err != nil || !reflect.DeepEqual(target.got, m) {
		t.Fatal("target metadata not forwarded", err)
	}
	got, err := d.AllowedUpdates(ctx, 1)
	if err != nil || !reflect.DeepEqual(got, []string{"message", "callback_query", "chat_member", "chat_join_request"}) {
		t.Fatal(got, err)
	}
	d.modules.Subscriptions = []UpdateSubscriptions{dispatchSubscription{"unsupported"}}
	if _, err := d.AllowedUpdates(ctx, 1); err == nil {
		t.Fatal("invalid subscription accepted")
	}
}

type shopBrandSettings struct{ contentSettingsStub }

func (shopBrandSettings) GetSiteBrand() (settingsapp.SiteBrand, error) {
	return settingsapp.SiteBrand{SiteURL: "https://shop.example.test"}, nil
}
func TestShopProductMediaAndWebURL(t *testing.T) {
	p := product.Product{Slug: "中文商品", Images: []string{"/uploads/image.png", "ignored"}, ContentJSON: jsonmap.JSON{"en-US": "Full markdown body"}, DescriptionJSON: jsonmap.JSON{"en-US": "Summary"}}
	v := productView(p, "en-US", &ShopDependencies{ContentSettings: shopBrandSettings{}})
	if v.Image != "/uploads/image.png" || v.URL != "https://shop.example.test/products/%E4%B8%AD%E6%96%87%E5%95%86%E5%93%81" || v.Description != "Full markdown body" || v.Summary != "Summary" {
		t.Fatalf("product content: %+v", v)
	}
}

func TestProductViewStructuredAvailabilityAndFullText(t *testing.T) {
	full := strings.Repeat("中文详情", 600)
	p := product.Product{DescriptionJSON: jsonmap.JSON{"en-US": full}, FulfillmentType: constants.FulfillmentTypeManual, MinPurchaseQuantity: 4,
		SKUs: []product.ProductSKU{{ID: 1, IsActive: true, ManualStockTotal: 3}, {ID: 2, IsActive: true, ManualStockTotal: 8}}}
	for _, mode := range []string{"exact", "range", "status", "hidden"} {
		p.StockDisplayMode = mode
		v := productView(p, "en-US")
		if v.Description != full || len([]rune(v.Summary)) != 501 || !v.Available || v.SKUs[0].Available || !v.SKUs[1].Available || v.FulfillmentType != p.FulfillmentType {
			t.Fatalf("structured product %s: %+v", mode, v)
		}
		if mode == "exact" {
			if v.MaxQuantity == nil || *v.MaxQuantity != 11 || v.SKUs[1].MaxQuantity == nil || *v.SKUs[1].MaxQuantity != 8 {
				t.Fatal("missing public exact bound")
			}
		} else if v.MaxQuantity != nil || v.SKUs[0].MaxQuantity != nil || v.SKUs[1].MaxQuantity != nil {
			t.Fatalf("%s leaked quantity", mode)
		}
	}
	p.StockDisplayMode = "exact"
	p.SKUs[1].ManualStockTotal = -1
	v := productView(p, "en-US")
	if !v.Available || v.MaxQuantity != nil || v.SKUs[1].MaxQuantity != nil {
		t.Fatal("unlimited bound")
	}
	p.FulfillmentType = constants.FulfillmentTypeUpstream
	v = productView(p, "en-US")
	if v.Available || v.MaxQuantity != nil || v.SKUs[1].Available {
		t.Fatal("unknown upstream availability invented")
	}
}

func TestOrderViewUsesSnapshotsWithoutDuplicatingAggregatedItems(t *testing.T) {
	item := order.OrderItem{ProductID: 10, SKUID: 20, Quantity: 3, TitleJSON: jsonmap.JSON{"en-US": "Old title"}, SKUSnapshotJSON: jsonmap.JSON{"spec_values": map[string]interface{}{"en-US": "Old SKU"}, "sku_code": "fallback"}}
	o := order.Order{CreatedAt: time.Now(), Children: []order.Order{{Items: []order.OrderItem{item}}}}
	for _, aggregate := range []bool{false, true} {
		if aggregate {
			o.Items = []order.OrderItem{item}
		}
		v := orderView(&o, "en-US")
		if len(v.Items) != 1 || v.Items[0].Title != "Old title" || v.Items[0].SKUName != "Old SKU" || v.Items[0].ProductID != 10 || v.Items[0].SKUID != 20 || v.Items[0].Quantity != 3 || !v.CreatedAt.Equal(o.CreatedAt) {
			t.Fatalf("snapshot: %+v", v)
		}
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

// linkRelationsStub supplies current membership configuration and per-target routes.
type linkRelationsStub struct {
	config               *memberdomain.ProductConfig
	targets              map[uint]*gd.Target
	configErr, targetErr error
}

// Config returns the selected configuration or injected storage error.
func (stub *linkRelationsStub) Config(context.Context, uint) (*memberdomain.ProductConfig, error) {
	return stub.config, stub.configErr
}

// Get returns the requested current target, never a historical snapshot.
func (stub *linkRelationsStub) Get(_ context.Context, id uint) (*gd.Target, error) {
	return stub.targets[id], stub.targetErr
}

// TestMembershipProductLinksCurrentAssociation validates status and all-target bot scoping.
func TestMembershipProductLinksCurrentAssociation(t *testing.T) {
	for _, mode := range []string{"valid", "same_bot_targets", "mixed_bots", "mixed_bots_reversed", "unrelated", "reassigned", "deleted_config", "disabled_config", "wrong_product", "no_targets", "wrong_relation", "missing_target", "disabled_target", "wrong_purpose", "recovering", "config_error", "target_error", "missing_wiring"} {
		t.Run(mode, func(t *testing.T) {
			target := &gd.Target{ID: 4, BotInstanceID: 7, Enabled: true, Purpose: "membership"}
			stub := &linkRelationsStub{config: &memberdomain.ProductConfig{ProductID: 123, Enabled: true, Quantity: 1, Targets: []memberdomain.ProductTarget{{ProductID: 123, TargetID: 4}}}, targets: map[uint]*gd.Target{4: target}}
			access := MembershipProductLinks{Configs: stub, Targets: stub}
			actor := c.Actor{BotID: 7}
			switch mode {
			case "same_bot_targets", "mixed_bots", "mixed_bots_reversed":
				stub.config.Targets = append(stub.config.Targets, memberdomain.ProductTarget{ProductID: 123, TargetID: 5})
				stub.targets[5] = &gd.Target{ID: 5, BotInstanceID: 7, Enabled: true, Purpose: "membership"}
				if mode != "same_bot_targets" {
					stub.targets[5].BotInstanceID = 8
				}
				if mode == "mixed_bots_reversed" {
					stub.config.Targets[0], stub.config.Targets[1] = stub.config.Targets[1], stub.config.Targets[0]
				}
			case "unrelated":
				actor.BotID = 8
			case "reassigned":
				target.BotInstanceID = 8
			case "deleted_config":
				stub.configErr = membership.ErrNotFound
			case "disabled_config":
				stub.config.Enabled = false
			case "wrong_product":
				stub.config.ProductID = 124
			case "no_targets":
				stub.config.Targets = nil
			case "wrong_relation":
				stub.config.Targets[0].ProductID = 124
			case "missing_target":
				stub.targetErr = groups.ErrNotFound
			case "disabled_target":
				target.Enabled = false
			case "wrong_purpose":
				target.Purpose = "support"
			case "recovering":
				target.RecoveryTaskID = 9
			case "config_error":
				stub.configErr = errors.New("storage unavailable")
			case "target_error":
				stub.targetErr = errors.New("storage unavailable")
			case "missing_wiring":
				access.Targets = nil
			}
			err := access.AuthorizeProductLink(context.Background(), actor, 123)
			if (err == nil) != (mode == "valid" || mode == "same_bot_targets") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}
