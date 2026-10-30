package application

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

func TestHTMLVisibleUTF16Budget(t *testing.T) {
	for _, budget := range []int{950, 3900} {
		input := "<b>" + html.EscapeString(strings.Repeat("𠮷<&", 2000)) + "</b>"
		out := limitHTML(input, budget)
		plain := html.UnescapeString(strings.ReplaceAll(strings.ReplaceAll(out, "<b>", ""), "</b>", ""))
		if len(utf16.Encode([]rune(plain))) > budget || strings.Contains(out, "<𠮷") {
			t.Fatal("invalid Telegram budget or escaping")
		}
	}
}
func TestRuntimePanelVersionsMatchEncryptedState(t *testing.T) {
	s, m, _ := setup()
	out := quoteFlow(t, s)
	st := savedState(t, s, m)
	row, _ := m.GetSession(context.Background(), 1, 11, 11)
	if row.PanelEpoch != st.PanelEpoch || row.PanelRevision != st.PanelRevision || out[0].PanelRevision != row.PanelRevision {
		t.Fatal("runtime would supersede panel")
	}
}
func TestPendingQueryOnlyToastsAndPreservesButtons(t *testing.T) {
	s, m, b := setup()
	out := quoteFlow(t, s)
	out = click(t, s, 6, out, "确认下单")
	before := savedState(t, s, m)
	u := update(7, "")
	u.CallbackData = button(t, out, "我已支付，查询结果")
	if msgs := run(t, s, u); len(msgs) != 0 {
		t.Fatal("pending query rerendered")
	}
	after := savedState(t, s, m)
	if after.PanelRevision != before.PanelRevision || after.Ref != before.Ref || b.payments != 1 {
		t.Fatal("query changed payment or panel version")
	}
	click(t, s, 8, out, "🏠 首页")
}
type tabbedCatalog struct{ displayCatalog }

func (tabbedCatalog) Categories(context.Context, c.Actor) ([]c.Category, error) {
	return []c.Category{{ID: 1, Name: "Cards"}, {ID: 2, Name: "Games"}}, nil
}
func (d tabbedCatalog) Products(context.Context, c.Actor, uint, int) ([]c.Product, bool, error) {
	other := d.product
	other.ID, other.Title = 3, "Other"
	return []c.Product{d.product, other}, false, nil
}
func TestCategoryTabsAreSeparatedFromProducts(t *testing.T) {
	s, m, _ := setup()
	s.d.Catalog = tabbedCatalog{displayCatalog{product: c.Product{ID: 2, Title: "Steam", Available: true, Price: "1.00", Currency: "CNY", Min: 1, Max: 10}}}
	out := run(t, s, update(1, "/start"))
	if out[0].Buttons[0][0].Style != stylePrimary {
		t.Fatal("shop entry is not the primary action")
	}
	out = click(t, s, 2, out, "🛍 逛商店")
	rows := out[0].Buttons
	if len(rows) < 3 || rows[0][0].Text != "✓ Cards" || rows[0][0].Style != stylePrimary || rows[1][0].Text != "— 商品 —" || !strings.HasPrefix(rows[2][0].Text, "Steam") {
		t.Fatalf("categories and products are not separated: %+v", rows)
	}
	before := savedState(t, s, m)
	u := update(3, "")
	u.CallbackData = rows[1][0].Data
	if msgs := run(t, s, u); len(msgs) != 0 {
		t.Fatal("divider rerendered the panel")
	}
	after := savedState(t, s, m)
	if after.Ref != before.Ref || after.PanelRevision != before.PanelRevision {
		t.Fatal("divider invalidated panel buttons")
	}
	click(t, s, 4, out, "Steam · ¥1.00")
}
func TestLanguageRestoresProductAndSelection(t *testing.T) {
	s, m, _ := setup()
	out := quoteFlow(t, s)
	before := savedState(t, s, m)
	out = run(t, s, update(6, "/language"))
	out = click(t, s, 7, out, "English")
	after := savedState(t, s, m)
	if after.Current.Kind != "purchase" || after.Selection != before.Selection || after.Operation != before.Operation || !strings.Contains(out[0].Text, "Total:") {
		t.Fatal("language reset purchase state")
	}
}

type filteredOrders struct {
	*backend
	owners  []uint
	filters []string
}

func (f *filteredOrders) PendingSummary(context.Context, c.Actor) (c.PendingSummary, error) {
	return c.PendingSummary{Count: 2, Latest: &c.Order{ID: 8, UserID: 7, Amount: "12", Currency: "CNY"}}, nil
}
func (f *filteredOrders) ListFiltered(_ context.Context, _ c.Actor, owner uint, _ int, status string) ([]c.Order, bool, error) {
	f.owners = append(f.owners, owner)
	f.filters = append(f.filters, status)
	return []c.Order{{ID: 8, UserID: owner, Number: "ORDER", Pending: true, Status: "pending_payment", Amount: "12", Currency: "CNY"}}, false, nil
}
func TestPendingSummaryUsesOptionalFilteredContract(t *testing.T) {
	s, _, b := setup()
	f := &filteredOrders{backend: b}
	s.d.Orders = f
	out := run(t, s, update(1, "/start"))
	if !strings.Contains(out[0].Text, "2 笔订单待支付") {
		t.Fatal(out)
	}
	out = click(t, s, 2, out, "继续支付")
	if len(f.filters) != 1 || f.filters[0] != "pending_payment" || f.owners[0] != 7 {
		t.Fatal(f.filters)
	}
	button(t, out, "ORDER · ")
}
func TestPersistedRuntimeTargetWinsAfterMediaReplacement(t *testing.T) {
	s, m, _ := setup()
	out := quoteFlow(t, s)
	u := update(6, "")
	u.CallbackData = button(t, out, "+")
	u.MessageID = 77
	out = run(t, s, u)
	row, _ := m.GetSession(context.Background(), 1, 11, 11)
	row.PanelMessageID = 88
	row.PanelMessageEpoch = row.PanelEpoch
	m.sessions[sessionKey(1, 11, 11)] = *row
	u = update(7, "")
	u.CallbackData = button(t, out, "+")
	u.MessageID = 88
	out = run(t, s, u)
	if len(out) != 1 || out[0].TargetMessageID != 88 || out[0].Purpose != "panel" {
		t.Fatal("current replaced target rejected", out)
	}
}
func TestOutdatedDeltaCannotChangeDifferentSKU(t *testing.T) {
	s, m, _ := setup()
	s.d.Catalog = displayCatalog{product: c.Product{ID: 2, Available: true, Min: 1, Max: 10, SKUs: []c.SKU{{ID: 3, Name: "A", Available: true}, {ID: 4, Name: "B", Available: true}}}}
	out := run(t, s, update(1, "/start"))
	out = click(t, s, 2, out, "🛍 逛商店")
	out = click(t, s, 3, out, "A ·  0.00")
	delta := button(t, out, "+")
	out = click(t, s, 4, out, "B ·  0.00")
	u := update(5, "")
	u.CallbackData = delta
	run(t, s, u)
	st := savedState(t, s, m)
	if st.Selection.SKUID != 4 || st.Selection.Quantity != 1 {
		t.Fatal("old SKU delta crossed context")
	}
}

func savedState(t *testing.T, s *Service, m *memoryStore) state {
	t.Helper()
	row, e := m.GetSession(context.Background(), 1, 11, 11)
	if e != nil || row == nil {
		t.Fatal(e)
	}
	raw, e := crypto.Decrypt(s.key, row.DataCipher)
	if e != nil {
		t.Fatal(e)
	}
	var st state
	if e = json.Unmarshal([]byte(raw), &st); e != nil {
		t.Fatal(e)
	}
	return st
}
func TestPanelQuantityBurstAndStaleConfirmation(t *testing.T) {
	s, m, b := setup()
	out := quoteFlow(t, s)
	oldConfirm := button(t, out, "确认下单")
	plus := button(t, out, "+")
	initial := savedState(t, s, m)
	for id := int64(6); id < 16; id++ {
		u := update(id, "")
		u.CallbackData = plus
		u.MessageID = 77
		out = run(t, s, u)
		if len(out) != 1 || out[0].Purpose != "panel" || out[0].Operation != "edit" || out[0].TargetMessageID != 77 {
			t.Fatalf("not one targeted panel: %+v", out)
		}
	}
	st := savedState(t, s, m)
	if st.Selection.Quantity != 12 || st.Ref != initial.Ref || st.PanelRevision != initial.PanelRevision+10 || st.Operation == initial.Operation {
		t.Fatalf("quantity context not cumulative: %+v", st)
	}
	u := update(15, "")
	u.CallbackData = plus
	run(t, s, u)
	if savedState(t, s, m).Selection.Quantity != 12 {
		t.Fatal("replayed update changed quantity")
	}
	u = update(16, "")
	u.CallbackData = oldConfirm
	run(t, s, u)
	if b.creates != 0 {
		t.Fatal("old confirmation created new selection")
	}
	u = update(14, "")
	u.CallbackData = plus
	if len(run(t, s, u)) != 0 || savedState(t, s, m).Selection.Quantity != 12 {
		t.Fatal("older update replayed")
	}
}
func TestPanelCleanupStampEpochAndEscaping(t *testing.T) {
	s, m, b := setup()
	s.d.Identity = &generationIdentity{backend: b, generation: "g:7"}
	s.d.Content = htmlContent{}
	u := update(1, "/start")
	u.MessageID = 42
	out := run(t, s, u)
	if len(out) != 2 || out[0].Purpose != "cleanup" || out[0].Operation != "delete" || out[0].TargetMessageID != 42 || out[0].IdentityStamp != "g:7" || out[0].OwnerUserID != 7 {
		t.Fatal(out)
	}
	panel := out[1]
	if panel.ParseMode != "HTML" || !panel.ReplacePanel || !strings.Contains(panel.Text, "&lt;Welcome&gt;") {
		t.Fatal(panel)
	}
	first := savedState(t, s, m)
	run(t, s, update(2, "/start"))
	if savedState(t, s, m).PanelEpoch == first.PanelEpoch {
		t.Fatal("start reused physical panel epoch")
	}
}

type htmlContent struct{}

func (htmlContent) Content(context.Context, c.Actor) (c.Content, error) {
	return c.Content{WelcomeEnabled: true, Welcome: "<Welcome> & hello"}, nil
}
func TestQuantityBoundsAndDetailsPreserveSelection(t *testing.T) {
	zero := 0
	three := 3
	p := &c.Product{Available: true, Min: 5, Max: 100, MaxQuantity: &three}
	if _, _, ok := quantityBounds(p, 0); ok {
		t.Fatal("minimum greater than stock buyable")
	}
	p.MaxQuantity = &zero
	if _, _, ok := quantityBounds(p, 0); ok {
		t.Fatal("zero stock buyable")
	}
	p.MaxQuantity = nil
	p.Min = 1
	if _, hi, ok := quantityBounds(p, 0); !ok || hi != 100 {
		t.Fatal("hidden stock treated as zero")
	}
	tiers := quantityTiers(2, 10, false)
	if len(tiers) != 3 || tiers[0] != 2 || tiers[2] != 10 {
		t.Fatal(tiers)
	}
	s, m, _ := setup()
	s.d.Catalog = displayCatalog{product: c.Product{ID: 2, Title: "<商品>", Available: true, Min: 1, Max: 100, Image: "/uploads/test.png", Description: strings.Repeat("描述<&", 2000)}}
	out := quoteFlow(t, s)
	before := savedState(t, s, m)
	if out[0].PhotoURL != "/uploads/test.png" || !strings.Contains(out[0].Text, "&lt;商品&gt;") {
		t.Fatal(out)
	}
	out = click(t, s, 6, out, "商品详情")
	if out[0].PhotoURL != "" || len([]rune(out[0].Text)) > 16000 {
		t.Fatal("details did not become bounded text")
	}
	out = click(t, s, 7, out, "返回选购")
	after := savedState(t, s, m)
	if after.Selection != before.Selection || after.Operation != before.Operation || out[0].PhotoURL == "" {
		t.Fatal("details lost selection")
	}
}

type mutableOrders struct {
	*backend
	order    c.Order
	cancels  int
	previews int
}

func (b *mutableOrders) Detail(_ context.Context, _ c.Actor, id, owner uint) (*c.Order, error) {
	o := b.order
	o.ID = id
	o.UserID = owner
	return &o, nil
}
func (b *mutableOrders) Cancel(_ context.Context, a c.Actor, id uint) error {
	if a.UserID != b.order.UserID {
		return c.ErrDenied
	}
	b.cancels++
	b.order.Pending = false
	b.order.Status = "cancelled"
	return nil
}
func (b *mutableOrders) Preview(ctx context.Context, a c.Actor, s c.Selection) (*c.Quote, error) {
	b.previews++
	return b.backend.Preview(ctx, a, s)
}
func TestCancellationRequiresSecondClickAndRebuyPreviews(t *testing.T) {
	s, _, b := setup()
	orders := &mutableOrders{backend: b, order: c.Order{ID: 8, UserID: 7, Number: "ORDER", Pending: true, Status: "pending_payment", Items: []c.OrderItem{{ProductID: 2, Quantity: 2}}}}
	s.d.Orders = orders
	out := quoteFlow(t, s)
	out = click(t, s, 6, out, "确认下单")
	out = click(t, s, 7, out, "取消订单")
	if orders.cancels != 0 || !strings.Contains(out[0].Text, "旧二维码") {
		t.Fatal("cancel missing confirmation risk")
	}
	out = click(t, s, 8, out, "确认取消")
	if orders.cancels != 1 {
		t.Fatal("not cancelled")
	}
	before := orders.previews
	out = click(t, s, 9, out, "重新购买")
	button(t, out, "确认下单")
	if orders.previews != before+1 {
		t.Fatal("rebuy skipped quote")
	}
}
func TestDeliveryExplicitDeduplicatedAndLongDocument(t *testing.T) {
	s, _, b := setup()
	orders := &mutableOrders{backend: b, order: c.Order{ID: 8, UserID: 7, Number: "ORDER", Status: "completed", Delivered: []string{strings.Repeat("secret", 700)}}}
	s.d.Orders = orders
	out := run(t, s, update(1, "/start"))
	out = click(t, s, 2, out, "📦 我的订单")
	out = click(t, s, 3, out, "ORDER · ")
	if len(out) != 1 || strings.Contains(out[0].Text, "secret") {
		t.Fatal("detail leaked delivery")
	}
	out = click(t, s, 4, out, "重新发送交付内容")
	count := 0
	for _, msg := range out {
		if msg.Purpose == "delivery" {
			count++
			if !msg.ForceDocument || msg.OwnerUserID != 7 {
				t.Fatal(msg)
			}
		}
	}
	if count != 1 {
		t.Fatal("no explicit document")
	}
	out = click(t, s, 5, out, "重新发送交付内容")
	for _, msg := range out {
		if msg.Purpose == "delivery" {
			t.Fatal("duplicate resend")
		}
	}
}

type mixedPayments struct {
	*backend
	channels []c.Channel
	paid     []uint
}

func (p *mixedPayments) Channels(context.Context, c.Actor, uint) ([]c.Channel, error) {
	return p.channels, nil
}
func (p *mixedPayments) Pay(_ context.Context, _ c.Actor, _ uint, ch uint, _ string) (*c.Payment, error) {
	p.paid = append(p.paid, ch)
	return &c.Payment{PayURL: "https://pay.example/checkout"}, nil
}
func TestWalletIsExplicitEvenWhenNonzeroID(t *testing.T) {
	s, _, b := setup()
	p := &mixedPayments{backend: b, channels: []c.Channel{{ID: 9, Name: "Wallet", Wallet: true}, {ID: 0, Name: "External"}}}
	s.d.Payments = p
	out := quoteFlow(t, s)
	out = click(t, s, 6, out, "确认下单")
	if len(p.paid) != 1 || p.paid[0] != 0 {
		t.Fatal("wallet auto charged or ID zero treated as wallet")
	}
	label := ""
	for _, row := range out[0].Buttons {
		for _, btn := range row {
			if strings.HasPrefix(btn.Text, "余额支付") {
				label = btn.Text
			}
		}
	}
	if label == "" {
		t.Fatal("wallet explicit button absent")
	}
	click(t, s, 7, out, label)
	if len(p.paid) != 2 || p.paid[1] != 9 {
		t.Fatal("explicit wallet was not used")
	}
}

type staticContent c.Content

func (s staticContent) Content(context.Context, c.Actor) (c.Content, error) { return c.Content(s), nil }

func TestWelcomeDisabledKeepsMenuAndSkipsDefaultCopy(t *testing.T) {
	s, _, _ := setup()
	s.d.Content = staticContent{WelcomeEnabled: false, HelpEnabled: false, Menu: []c.MenuItem{{Action: "catalog", Label: "Buy"}, {Action: "help", Label: "Hidden help"}}}
	out := run(t, s, update(1, "/start"))
	if len(out) != 1 || strings.Contains(out[0].Text, "欢迎光临") || out[0].PhotoURL != "" {
		t.Fatalf("welcome disabled: %+v", out)
	}
	for _, row := range out[0].Buttons {
		for _, btn := range row {
			if btn.Text == "Hidden help" {
				t.Fatal("closed help remained on home")
			}
		}
	}
	out = run(t, s, update(2, "/help"))
	if len(out) != 1 || !strings.Contains(out[0].Text, "帮助中心已关闭") {
		t.Fatalf("help closed: %+v", out)
	}
}

func TestHelpListsItemsInsteadOfMergedBodies(t *testing.T) {
	s, _, _ := setup()
	s.d.Content = staticContent{
		WelcomeEnabled: true, HelpEnabled: true, CenterHint: "Topics", SupportURL: "https://support.example/help",
		HelpItems: []c.HelpItem{{Key: "pay", Summary: "Payment FAQ", Title: "How to pay", Body: "Scan the QR", ShowSupport: true}},
		Menu:      []c.MenuItem{{Action: "help", Label: "Help"}},
	}
	out := run(t, s, update(1, "/help"))
	if len(out) != 1 || !strings.Contains(out[0].Text, "Topics") || strings.Contains(out[0].Text, "Scan the QR") {
		t.Fatalf("help list leaked bodies: %+v", out)
	}
	out = click(t, s, 2, out, "Payment FAQ")
	if len(out) != 1 || !strings.Contains(out[0].Text, "Scan the QR") {
		t.Fatalf("help detail missing: %+v", out)
	}
	found := false
	for _, row := range out[0].Buttons {
		for _, btn := range row {
			if btn.URL == "https://support.example/help" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("support link missing")
	}
}

func TestOrderEventRefreshDoesNotNavigateAndCASFences(t *testing.T) {
	s, m, _ := setup()
	out := quoteFlow(t, s)
	out = click(t, s, 6, out, "确认下单")
	refreshed, e := s.RefreshOrderPanel(context.Background(), 1, 11, 8)
	if e != nil || len(refreshed) != 1 || refreshed[0].Purpose != "panel" {
		t.Fatalf("refresh: %+v %v", refreshed, e)
	}
	m.fail = true
	if _, e = s.RefreshOrderPanel(context.Background(), 1, 11, 8); !errors.Is(e, rt.ErrConflict) {
		t.Fatal("event refresh bypassed CAS")
	}
	out = click(t, s, 7, refreshed, "🏠 首页")
	_ = out
	if msgs, e := s.RefreshOrderPanel(context.Background(), 1, 11, 8); e != nil || len(msgs) != 0 {
		t.Fatal("event hijacked homepage")
	}
}
func TestBindFailureKeepsEmailAndDeletesCode(t *testing.T) {
	s, m, b := setup()
	s.d.Identity = badCodeIdentity{b}
	out := run(t, s, update(1, "/start"))
	out = click(t, s, 2, out, "👤 账户余额")
	out = click(t, s, 3, out, "绑定已有账号")
	out = run(t, s, update(4, "a@example.com"))
	u := update(5, "123456")
	u.MessageID = 51
	out = run(t, s, u)
	st := savedState(t, s, m)
	if st.Email != "a@example.com" || st.Step != "code" || out[0].Purpose != "cleanup" {
		t.Fatal(st, out)
	}
	button(t, out, "重新发送验证码")
}

type badCodeIdentity struct{ *backend }

func (badCodeIdentity) Bind(context.Context, c.Actor, string, string) (uint, error) {
	return 0, c.ErrDenied
}
func TestOldPhysicalPanelOnlyCleansWithoutBusiness(t *testing.T) {
	s, _, b := setup()
	out := quoteFlow(t, s)
	u := update(6, "")
	u.MessageID = 77
	u.CallbackData = button(t, out, "+")
	out = run(t, s, u)
	u = update(7, "")
	u.MessageID = 66
	u.CallbackData = "s:old:0:no"
	out = run(t, s, u)
	if len(out) != 1 || out[0].Purpose != "cleanup" || b.creates != 0 {
		t.Fatal(out)
	}
}
func TestExpiredSessionCannotRecoverCheckout(t *testing.T) {
	s, _, b := setup()
	out := quoteFlow(t, s)
	u := update(6, "")
	u.CallbackData = button(t, out, "确认下单")
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	run(t, s, u)
	if b.creates != 0 {
		t.Fatal("expired checkout performed write")
	}
}
