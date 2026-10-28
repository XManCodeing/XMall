package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type memoryStore struct {
	sessions map[string]rd.Session
	contacts map[int64]rd.Contact
	fail     bool
}

func sessionKey(bot uint, user, chat int64) string {
	return strings.Join([]string{string(rune(bot)), string(rune(user)), string(rune(chat))}, ":")
}
func (m *memoryStore) GetSession(_ context.Context, b uint, u, ch int64) (*rd.Session, error) {
	s, ok := m.sessions[sessionKey(b, u, ch)]
	if !ok {
		return nil, nil
	}
	return &s, nil
}
func (m *memoryStore) SaveSession(_ context.Context, s *rd.Session, version uint64) error {
	if m.fail {
		m.fail = false
		return rt.ErrConflict
	}
	k := sessionKey(s.BotID, s.TelegramUserID, s.ChatID)
	if m.sessions[k].Version != version {
		return rt.ErrConflict
	}
	s.Version = version + 1
	m.sessions[k] = *s
	return nil
}
func (m *memoryStore) Contact(_ context.Context, b uint, u int64) (*rd.Contact, error) {
	v, ok := m.contacts[u]
	if !ok || v.BotID != b {
		return nil, nil
	}
	return &v, nil
}
func (m *memoryStore) SaveContact(_ context.Context, c *rd.Contact) error {
	m.contacts[c.TelegramUserID] = *c
	return nil
}

type backend struct {
	user     uint
	history  []uint
	quote    c.Quote
	creates  int
	keys     []string
	created  map[string]*c.Order
	details  int
	payments int
}

func (b *backend) Resolve(context.Context, c.Actor, bool) (uint, error)        { return b.user, nil }
func (b *backend) SendBindCode(context.Context, c.Actor, string) error         { return nil }
func (b *backend) Bind(context.Context, c.Actor, string, string) (uint, error) { return b.user, nil }
func (b *backend) HistoryUsers(context.Context, c.Actor) ([]uint, error)       { return b.history, nil }
func (b *backend) Categories(context.Context, c.Actor) ([]c.Category, error) {
	return []c.Category{{ID: 1, Name: "Category"}}, nil
}
func (b *backend) Products(context.Context, c.Actor, uint, int) ([]c.Product, bool, error) {
	return []c.Product{{ID: 2, Title: "Product"}}, false, nil
}
func (b *backend) Product(context.Context, c.Actor, uint) (*c.Product, error) {
	return &c.Product{ID: 2, Title: "Product", Min: 1, Max: 10}, nil
}
func (b *backend) Preview(context.Context, c.Actor, c.Selection) (*c.Quote, error) {
	q := b.quote
	return &q, nil
}
func (b *backend) Create(_ context.Context, a c.Actor, _ c.Selection, q c.Quote, key string) (*c.Order, error) {
	b.keys = append(b.keys, key)
	if o := b.created[key]; o != nil {
		return o, nil
	}
	if q != b.quote {
		return nil, c.ErrPriceChanged
	}
	b.creates++
	o := &c.Order{ID: 8, UserID: a.UserID, Number: "ORDER", Pending: true}
	b.created[key] = o
	return o, nil
}
func (b *backend) List(_ context.Context, a c.Actor, owner uint, page int) ([]c.Order, bool, error) {
	return []c.Order{{ID: 8, UserID: owner, Number: "ORDER"}}, false, nil
}
func (b *backend) Detail(_ context.Context, a c.Actor, id, owner uint) (*c.Order, error) {
	b.details++
	return &c.Order{ID: id, UserID: owner, Number: "ORDER", Pending: true, Delivered: []string{"SECRET"}}, nil
}
func (b *backend) Channels(context.Context, c.Actor, uint) ([]c.Channel, error) {
	return []c.Channel{{ID: 1, Name: "Pay"}}, nil
}
func (b *backend) Pay(context.Context, c.Actor, uint, uint, string) (*c.Payment, error) {
	b.payments++
	return &c.Payment{PayURL: "https://pay.example/checkout"}, nil
}
func (b *backend) Account(_ context.Context, a c.Actor) (*c.Account, error) {
	return &c.Account{ID: a.UserID, Balance: "10.00"}, nil
}
func setup() (*Service, *memoryStore, *backend) {
	m := &memoryStore{sessions: map[string]rd.Session{}, contacts: map[int64]rd.Contact{}}
	b := &backend{user: 7, quote: c.Quote{Amount: "12.00", Currency: "CNY", Fingerprint: "stable"}, created: map[string]*c.Order{}}
	return New(Dependencies{Store: m, Identity: b, Catalog: b, Orders: b, Payments: b, Accounts: b, Secret: "test-secret"}), m, b
}
func update(id int64, text string) rt.Update {
	return rt.Update{ID: id, UserID: 11, ChatID: 11, Private: true, Text: text}
}
func run(t *testing.T, s *Service, u rt.Update) []rt.Message {
	t.Helper()
	msgs, e := s.Handle(context.Background(), 1, u)
	if e != nil {
		t.Fatal(e)
	}
	return msgs
}
func button(t *testing.T, msgs []rt.Message, label string) string {
	t.Helper()
	for _, m := range msgs {
		for _, row := range m.Buttons {
			for _, b := range row {
				if b.Text == label {
					return b.Data
				}
			}
		}
	}
	t.Fatalf("missing button %q", label)
	return ""
}
func click(t *testing.T, s *Service, id int64, msgs []rt.Message, label string) []rt.Message {
	u := update(id, "")
	u.CallbackData = button(t, msgs, label)
	return run(t, s, u)
}
func quoteFlow(t *testing.T, s *Service) []rt.Message {
	m := run(t, s, update(1, "/start"))
	m = click(t, s, 2, m, "商品目录")
	m = click(t, s, 3, m, "Category")
	m = click(t, s, 4, m, "Product")
	return run(t, s, update(5, "2"))
}

type displayCatalog struct{ product c.Product }

func (d displayCatalog) Categories(context.Context, c.Actor) ([]c.Category, error) {
	return []c.Category{{ID: 1, Name: "Category"}}, nil
}
func (d displayCatalog) Products(context.Context, c.Actor, uint, int) ([]c.Product, bool, error) {
	return []c.Product{d.product}, false, nil
}
func (d displayCatalog) Product(context.Context, c.Actor, uint) (*c.Product, error) {
	p := d.product
	return &p, nil
}

func TestCatalogDisplaysPublicPricesAndUsesPreviewForPayableAmount(t *testing.T) {
	s, _, _ := setup()
	s.d.Catalog = displayCatalog{product: c.Product{ID: 2, Title: "Product", Price: "25.00", Currency: "CNY", Stock: "库存: 6–20", Min: 1, Max: 10, SKUs: []c.SKU{{ID: 3, Name: "SKU", Price: "30.00", Stock: "库存: 6–20"}}}}
	msgs := run(t, s, update(1, "/start"))
	msgs = click(t, s, 2, msgs, "商品目录")
	msgs = click(t, s, 3, msgs, "Category")
	msgs = click(t, s, 4, msgs, "Product · CNY 25.00 · 库存: 6–20")
	joined := func(messages []rt.Message) string {
		var parts []string
		for _, message := range messages {
			parts = append(parts, message.Text)
		}
		return strings.Join(parts, "\n")
	}
	if !strings.Contains(joined(msgs), "公开价: CNY 25.00") || !strings.Contains(joined(msgs), "订单预览") {
		t.Fatalf("missing price qualification: %+v", msgs)
	}
	msgs = click(t, s, 5, msgs, "SKU · CNY 30.00 · 库存: 6–20")
	if !strings.Contains(joined(msgs), "CNY 30.00") || !strings.Contains(joined(msgs), "库存: 6–20") {
		t.Fatalf("missing SKU display: %+v", msgs)
	}
	msgs = run(t, s, update(6, "2"))
	if !strings.Contains(msgs[0].Text, "12.00") || strings.Contains(msgs[0].Text, "60.00") {
		t.Fatalf("quote did not use preview amount: %+v", msgs)
	}
}

func TestCheckoutReplayCASAndEncryptedSession(t *testing.T) {
	s, m, b := setup()
	msgs := quoteFlow(t, s)
	u := update(6, "")
	u.CallbackData = button(t, msgs, "确认下单")
	m.fail = true
	if _, e := s.Handle(context.Background(), 1, u); !errors.Is(e, rt.ErrConflict) {
		t.Fatal(e)
	}
	b.quote.Amount = "999.00" // committed operation must replay even after repricing
	out := run(t, s, u)
	replay := run(t, s, u)
	if !reflect.DeepEqual(out, replay) {
		t.Fatal("reply replay differs")
	}
	if b.creates != 1 || len(b.keys) != 2 || b.keys[0] != b.keys[1] {
		t.Fatalf("create is not stable: %+v", b.keys)
	}
	row, _ := m.GetSession(context.Background(), 1, 11, 11)
	if strings.Contains(row.DataCipher, "SECRET") {
		t.Fatal("plaintext secret")
	}
	plain, e := crypto.Decrypt(s.key, row.DataCipher)
	if e != nil || !strings.Contains(plain, "SECRET") {
		t.Fatal("encrypted state missing")
	}
}
func TestQuoteChangeRefusesCreation(t *testing.T) {
	s, _, b := setup()
	msgs := quoteFlow(t, s)
	b.quote.Amount = "13.00"
	out := click(t, s, 6, msgs, "确认下单")
	if b.creates != 0 || !strings.Contains(out[0].Text, "报价已变化") {
		t.Fatal("changed quote accepted")
	}
	click(t, s, 7, out, "确认下单")
	if b.creates != 1 || len(b.keys) != 2 || b.keys[0] == b.keys[1] {
		t.Fatal("new quote did not require a fresh confirmed operation")
	}
}
func TestPrivateOnlyAndCallbackOwnership(t *testing.T) {
	s, m, _ := setup()
	u := update(1, "/start")
	u.Private = false
	u.ChatID = -123
	if out, e := s.Handle(context.Background(), 1, u); e != nil || len(out) != 0 || len(m.sessions) != 0 {
		t.Fatal("group handled")
	}
	msgs := run(t, s, update(2, "/start"))
	u = update(3, "")
	u.UserID = 22
	u.ChatID = 22
	u.CallbackData = button(t, msgs, "商品目录")
	out := run(t, s, u)
	if !strings.Contains(out[0].Text, "过期") {
		t.Fatal("cross-user callback accepted")
	}
}
func TestStaleAndExpiredCallbacks(t *testing.T) {
	s, _, _ := setup()
	old := run(t, s, update(1, "/start"))
	run(t, s, update(2, "/start"))
	u := update(3, "")
	u.CallbackData = button(t, old, "商品目录")
	out := run(t, s, u)
	if !strings.Contains(out[0].Text, "过期") {
		t.Fatal("stale callback accepted")
	}
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	out = run(t, s, u)
	if !strings.Contains(out[0].Text, "过期") {
		t.Fatal("expired callback accepted")
	}
}
func TestHistoryReadOnlyAndRevocation(t *testing.T) {
	s, _, b := setup()
	b.history = []uint{3}
	msgs := run(t, s, update(1, "/start"))
	msgs = click(t, s, 2, msgs, "绑定前历史订单（只读）")
	msgs = click(t, s, 3, msgs, "#3")
	msgs = click(t, s, 4, msgs, "ORDER · ")
	for _, m := range msgs {
		if m.OwnerUserID != 3 {
			t.Fatalf("history reply owner = %d", m.OwnerUserID)
		}
		for _, r := range m.Buttons {
			for _, btn := range r {
				if btn.Text == "选择支付渠道" {
					t.Fatal("history mutation exposed")
				}
			}
		}
	}
	if e := s.AuthorizeDelivery(context.Background(), 1, 11, 3); e != nil {
		t.Fatal(e)
	}
	b.history = nil
	replay := update(4, "")
	if _, err := s.Handle(context.Background(), 1, replay); !errors.Is(err, c.ErrDenied) {
		t.Fatal("cached history leaked after revocation")
	}
	if e := s.AuthorizeDelivery(context.Background(), 1, 11, 3); !errors.Is(e, c.ErrDenied) {
		t.Fatal("revoked history authorized")
	}
	u := update(5, "")
	u.CallbackData = button(t, msgs, "查询订单 / 支付结果")
	run(t, s, u)
	if b.details != 1 {
		t.Fatal("revoked history queried")
	}
}

type historyBackend struct{ *backend }

func (b historyBackend) HistoryAccount(context.Context, c.Actor, uint) (*c.Account, error) {
	return &c.Account{ID: 3, Balance: "15.00", Currency: "CNY"}, nil
}
func (b historyBackend) HistoryRefunds(context.Context, c.Actor, uint, int) ([]c.Refund, bool, error) {
	return []c.Refund{{Amount: "2.00", Currency: "CNY", Reference: "refund-order"}}, false, nil
}
func TestHistoricalWalletRemainsReadOnlyAndRevocable(t *testing.T) {
	s, _, b := setup()
	b.history = []uint{3}
	s.d.Accounts = historyBackend{b}
	msgs := run(t, s, update(1, "/start"))
	msgs = click(t, s, 2, msgs, "绑定前历史订单（只读）")
	msgs = click(t, s, 3, msgs, "#3")
	msgs = click(t, s, 4, msgs, "原账号余额（只读，不合并）")
	for _, m := range msgs {
		if m.OwnerUserID != 3 {
			t.Fatal("historical wallet reply lacks owner fence")
		}
	}
	msgs = click(t, s, 5, msgs, "原账号余额退款记录（只读）")
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Text, "refund-order") {
			found = true
		}
		if m.OwnerUserID != 3 {
			t.Fatal("refund owner fence lost")
		}
	}
	if !found {
		t.Fatal("refund record missing")
	}
	b.history = nil
	if err := s.AuthorizeDelivery(context.Background(), 1, 11, 3); !errors.Is(err, c.ErrDenied) {
		t.Fatal("revoked historical wallet still authorized")
	}
}

func TestPaymentMessageIncludesOrderAndDeadline(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Hour)
	for _, paid := range []bool{false, true} {
		v := &view{service: &Service{now: func() time.Time { return now }}, session: &rd.Session{}, state: &state{Actions: map[string]action{}}, actor: c.Actor{Locale: "en-US"}}
		p := &c.Payment{OrderNumber: "ORDER-123", Amount: "10", Currency: "CNY", QRCode: "https://pay.example/scan", ExpiresAt: &deadline, Paid: paid}
		if err := v.payment(p, 1); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(v.messages[0].Text, p.OrderNumber) {
			t.Fatal("missing order number")
		}
		if !paid && !strings.Contains(v.messages[0].Text, deadline.Format(time.RFC3339)) {
			t.Fatal("missing deadline")
		}
		p.Paid = false
		p.ExpiresAt = &now
		if err := v.payment(p, 1); !errors.Is(err, c.ErrUnavailable) {
			t.Fatalf("expired payment accepted: %v", err)
		}
	}
}

func TestPaymentMedia(t *testing.T) {
	photo, link, err := PaymentMedia(c.Payment{InteractionMode: "redirect", PayURL: "https://pay.example/checkout", QRCode: "unused-qr"})
	if err != nil || len(photo) != 0 || link == "" {
		t.Fatal("redirect mode must use checkout")
	}
	photo, link, err = PaymentMedia(c.Payment{InteractionMode: "qr", PayURL: "https://pay.example/scan"})
	if err != nil || len(photo) == 0 || link == "" {
		t.Fatal("QR mode must encode gateway payload")
	}
	cases := []struct {
		name              string
		p                 c.Payment
		photo, link, fail bool
	}{{"code", c.Payment{QRCode: "weixin://wxpay/bizpayurl?pr=test"}, true, false, false}, {"urlscheme", c.Payment{PayURL: "alipays://platformapi/startapp?appId=1"}, true, false, false}, {"post bridge", c.Payment{PayURL: "https://shop.example/api/v1/payments/epay-redirect?payment_id=1&token=test"}, false, true, false}, {"remote image", c.Payment{QRCode: "http://127.0.0.1/private.png", QRCodeKind: "image"}, false, false, true}, {"unsafe", c.Payment{PayURL: "javascript:alert(1)"}, false, false, true}}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p, l, e := PaymentMedia(tt.p)
			if (len(p) > 0) != tt.photo || (l != "") != tt.link || (e != nil) != tt.fail {
				t.Fatalf("photo=%d link=%q err=%v", len(p), l, e)
			}
		})
	}
}
