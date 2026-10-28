package application

import (
	"context"
	"errors"
	"testing"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type configuredContent struct{}

func (configuredContent) Content(_ context.Context, a c.Actor) (c.Content, error) {
	return c.Content{Welcome: "Welcome " + a.Locale, Help: "Configured help " + a.Locale, Menu: []c.MenuItem{{Action: "catalog", Label: "Browse"}, {Action: "help", Label: "Help"}, {Action: "language", Label: "Language"}, {Action: "relay", Label: "UNIMPLEMENTED"}, {URL: "javascript:alert(1)", Label: "BAD"}}}, nil
}
func TestConfiguredContentCommandsAndCallbacks(t *testing.T) {
	s, _, _ := setup()
	s.d.Content = configuredContent{}
	home := run(t, s, update(1, "/start"))
	if home[0].Text != "Welcome zh-CN" {
		t.Fatal(home)
	}
	if len(home[0].Buttons) != 5 {
		t.Fatal("unsupported or unsafe menu exposed")
	}
	help := click(t, s, 2, home, "Help")
	if help[0].Text != "Configured help zh-CN" {
		t.Fatal(help)
	}
	home = click(t, s, 3, help, "返回主菜单 / 取消")
	if home[0].Text != "Welcome zh-CN" {
		t.Fatal("callback home not configured")
	}
	lang := click(t, s, 4, home, "Language")
	home = click(t, s, 5, lang, "en-US")
	if home[0].Text != "Welcome en-US" {
		t.Fatal("locale content not refreshed")
	}
	help = run(t, s, update(6, "/help"))
	if help[0].Text != "Configured help en-US" {
		t.Fatal(help)
	}
}

type generationIdentity struct {
	*backend
	generation string
}

func (b *generationIdentity) Generation(context.Context, c.Actor) (string, error) {
	return b.generation, nil
}
func TestRecreatedIdentityInvalidatesOldCheckout(t *testing.T) {
	s, _, b := setup()
	identity := &generationIdentity{backend: b, generation: "1:7"}
	s.d.Identity = identity
	m := quoteFlow(t, s)
	if m[0].IdentityStamp != "1:7" {
		t.Fatal("reply missing binding generation")
	}
	identity.generation = "2:7"
	if e := s.AuthorizeStamp(context.Background(), 1, 11, "1:7"); !errors.Is(e, c.ErrDenied) {
		t.Fatal("old binding delivery authorized")
	}
	u := update(6, "")
	u.CallbackData = button(t, m, "确认下单")
	out := run(t, s, u)
	if b.creates != 0 || out[0].Text != text("zh-CN", "stale") {
		t.Fatal("recreated identity reused old operation")
	}
}

type uncertainPayments struct{ *backend }

func (p uncertainPayments) Pay(context.Context, c.Actor, uint, uint, string) (*c.Payment, error) {
	return nil, c.ErrPaymentUncertain
}
func TestPaymentUncertainOffersReadOnlyStatus(t *testing.T) {
	s, _, b := setup()
	s.d.Payments = uncertainPayments{b}
	m := quoteFlow(t, s)
	m = click(t, s, 6, m, "确认下单")
	m = click(t, s, 7, m, "选择支付渠道")
	m = click(t, s, 8, m, "Pay")
	if m[0].Text != text("zh-CN", "payment_uncertain") {
		t.Fatal(m)
	}
	button(t, m, "查询订单 / 支付结果")
	for _, msg := range m {
		for _, row := range msg.Buttons {
			for _, btn := range row {
				if btn.URL != "" {
					t.Fatal("uncertain creation exposed unverified payment link")
				}
			}
		}
	}
}

type paymentRecorder struct {
	*backend
	keys []string
}

func (p *paymentRecorder) Pay(_ context.Context, _ c.Actor, _ uint, _ uint, key string) (*c.Payment, error) {
	p.keys = append(p.keys, key)
	return &c.Payment{PayURL: "https://pay.example/checkout", Amount: "12.00", Currency: "CNY"}, nil
}
func TestPaymentOperationStableAcrossCASFailure(t *testing.T) {
	s, store, b := setup()
	p := &paymentRecorder{backend: b}
	s.d.Payments = p
	m := quoteFlow(t, s)
	m = click(t, s, 6, m, "确认下单")
	m = click(t, s, 7, m, "选择支付渠道")
	u := update(8, "")
	u.CallbackData = button(t, m, "Pay")
	store.fail = true
	if _, e := s.Handle(context.Background(), 1, u); !errors.Is(e, rt.ErrConflict) {
		t.Fatal(e)
	}
	run(t, s, u)
	if len(p.keys) != 2 || p.keys[0] == "" || p.keys[0] != p.keys[1] {
		t.Fatalf("unstable payment operation: %+v", p.keys)
	}
}
