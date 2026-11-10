package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

// retryPayments models immutable durable attempts: an old key never dispatches
// again, but returns the same authorized successor after a terminal failure.
type retryPayments struct {
	*backend
	channels   []c.Channel
	keys       []string
	active     string
	attempts   map[string]bool
	fail       bool
	dispatches int
}

// Channels returns the test channels without network or persistence effects.
func (p *retryPayments) Channels(context.Context, c.Actor, uint) ([]c.Channel, error) {
	return p.channels, nil
}

// Pay records keys and emulates terminal/successful replay for the requested key.
func (p *retryPayments) Pay(_ context.Context, _ c.Actor, _ uint, _ uint, key string) (*c.Payment, error) {
	p.keys = append(p.keys, key)
	if terminal, exists := p.attempts[key]; exists {
		if terminal || p.active != key {
			return nil, &c.PaymentRetryError{OperationKey: key + ":retry"}
		}
	} else {
		p.dispatches++
		p.attempts[key] = p.fail
		if p.fail {
			return nil, &c.PaymentRetryError{OperationKey: key + ":retry"}
		}
		p.active = key
	}
	return &c.Payment{PayURL: "https://pay.example/" + fmt.Sprint(p.dispatches), Amount: "12.00", Currency: "CNY"}, nil
}

// TestPaymentRetryPersistsAcrossReentry checks single-channel automatic failure,
// explicit retry, navigation reentry and duplicate callbacks with recovered config.
func TestPaymentRetryPersistsAcrossReentry(t *testing.T) {
	for _, reenter := range []bool{false, true} {
		t.Run(fmt.Sprint(reenter), func(t *testing.T) {
			s, _, b := setup()
			p := &retryPayments{backend: b, channels: []c.Channel{{ID: 1, Name: "A"}}, attempts: map[string]bool{}, fail: true}
			s.d.Payments = p
			messages := click(t, s, 6, quoteFlow(t, s), "确认下单")
			if !strings.Contains(messages[0].Text, text("zh-CN", "payment_retry")) || p.dispatches != 1 {
				t.Fatalf("missing retry feedback: %+v", messages)
			}
			button(t, messages, text("zh-CN", "retry_payment"))
			p.fail = false
			nextID := int64(7)
			label := text("zh-CN", "retry_payment")
			if reenter {
				messages = click(t, s, nextID, messages, text("zh-CN", "change_payment"))
				nextID++
				label = "A"
			}
			callback := update(nextID, "")
			callback.CallbackData = button(t, messages, label)
			run(t, s, callback)
			run(t, s, callback)
			if p.dispatches != 2 || len(p.keys) != 2 || p.keys[0] == p.keys[1] {
				t.Fatalf("retry keys=%v dispatches=%d", p.keys, p.dispatches)
			}
		})
	}
}

// TestPaymentTerminalRetrySurvivesSessionCASFailure verifies the retry button is
// regenerated deterministically when durable session persistence fails.
func TestPaymentTerminalRetrySurvivesSessionCASFailure(t *testing.T) {
	s, store, b := setup()
	p := &retryPayments{backend: b, channels: []c.Channel{{ID: 1, Name: "A"}}, attempts: map[string]bool{}, fail: true}
	s.d.Payments = p
	messages := quoteFlow(t, s)
	callback := update(6, "")
	callback.CallbackData = button(t, messages, "确认下单")
	store.fail = true
	if _, err := s.Handle(context.Background(), 1, callback); !errors.Is(err, rt.ErrConflict) {
		t.Fatal(err)
	}
	messages = run(t, s, callback)
	if p.dispatches != 1 || len(p.keys) != 2 || p.keys[0] != p.keys[1] {
		t.Fatalf("CAS retry duplicated: %+v", p)
	}
	p.fail = false
	messages = click(t, s, 7, messages, text("zh-CN", "retry_payment"))
	if p.dispatches != 2 || len(messages) == 0 {
		t.Fatalf("retry did not proceed: %+v", p)
	}
}

// TestShopPaymentChannelRoundTrip uses real session routing for A -> B -> A.
// The superseded A entry is replaced by explicit retry feedback, never a URL.
func TestShopPaymentChannelRoundTrip(t *testing.T) {
	s, _, b := setup()
	p := &retryPayments{backend: b, channels: []c.Channel{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}, attempts: map[string]bool{}}
	s.d.Payments = p
	messages := click(t, s, 6, quoteFlow(t, s), "确认下单")
	messages = click(t, s, 7, messages, "A")
	messages = click(t, s, 8, messages, text("zh-CN", "change_payment"))
	messages = click(t, s, 9, messages, "B")
	messages = click(t, s, 10, messages, text("zh-CN", "change_payment"))
	messages = click(t, s, 11, messages, "A")
	if p.dispatches != 2 {
		t.Fatalf("old A dispatched again: %d", p.dispatches)
	}
	for _, message := range messages {
		for _, row := range message.Buttons {
			for _, button := range row {
				if button.URL != "" {
					t.Fatal("old payment URL exposed")
				}
			}
		}
	}
	messages = click(t, s, 12, messages, text("zh-CN", "retry_payment"))
	if p.dispatches != 3 || len(messages) == 0 {
		t.Fatal("new A not created")
	}
}

type configuredContent struct{}

func (configuredContent) Content(_ context.Context, a c.Actor) (c.Content, error) {
	return c.Content{WelcomeEnabled: true, HelpEnabled: true, Welcome: "Welcome " + a.Locale, Help: "Configured help " + a.Locale, Menu: []c.MenuItem{{Action: "catalog", Label: "Browse"}, {Action: "help", Label: "Help"}, {Action: "language", Label: "Language"}, {Action: "relay", Label: "UNIMPLEMENTED"}, {URL: "javascript:alert(1)", Label: "BAD"}}}, nil
}
func TestConfiguredContentCommandsAndCallbacks(t *testing.T) {
	s, _, _ := setup()
	s.d.Content = configuredContent{}
	home := run(t, s, update(1, "/start"))
	if home[0].Text != "Welcome zh-CN" {
		t.Fatal(home)
	}
	if len(home[0].Buttons) != 2 {
		t.Fatal("unsupported or unsafe menu exposed")
	}
	help := click(t, s, 2, home, "Help")
	if help[0].Text != "Configured help zh-CN" {
		t.Fatal(help)
	}
	home = click(t, s, 3, help, "🏠 首页")
	if home[0].Text != "Welcome zh-CN" {
		t.Fatal("callback home not configured")
	}
	lang := click(t, s, 4, home, "Language")
	home = click(t, s, 5, lang, "English")
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
	if b.creates != 0 || out[0].Text != text("zh-CN", "welcome") {
		t.Fatal("recreated identity reused old operation")
	}
}

type insufficientPayments struct{ *backend }

// Pay simulates an authoritative wallet shortfall without a gateway attempt.
func (payments insufficientPayments) Pay(context.Context, c.Actor, uint, uint, string) (*c.Payment, error) {
	return nil, c.ErrInsufficientBalance
}

// TestInsufficientBalanceFeedback checks checkout error consumption and three-language text.
func TestInsufficientBalanceFeedback(t *testing.T) {
	service, _, backend := setup()
	service.d.Payments = insufficientPayments{backend}
	messages := click(t, service, 6, quoteFlow(t, service), "确认下单")
	if len(messages) == 0 || messages[0].Text != text("zh-CN", "insufficient_balance") {
		t.Fatalf("wallet failure not actionable: %+v", messages)
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		message := text(locale, "insufficient_balance")
		if message == "" || message == "insufficient_balance" || message == text(locale, "failed") {
			t.Fatalf("missing balance feedback for %s: %q", locale, message)
		}
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
	if m[0].Text != text("zh-CN", "payment_uncertain") {
		t.Fatal(m)
	}
	button(t, m, "我已支付，查询结果")
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

// TestPaymentUnknownRefreshDoesNotDispatch checks stale session uncertainty is only a display fence.
// Refresh performs reads; another explicit payment still reaches the authoritative unknown guard.
func TestPaymentUnknownRefreshDoesNotDispatch(t *testing.T) {
	s, _, backend := setup()
	s.d.Payments = uncertainPayments{backend}
	messages := click(t, s, 6, quoteFlow(t, s), "确认下单")
	messages = click(t, s, 7, messages, text("zh-CN", "refresh"))
	if backend.payments != 0 {
		t.Fatal("refresh dispatched payment")
	}
	for _, message := range messages {
		for _, row := range message.Buttons {
			for _, button := range row {
				if button.URL != "" {
					t.Fatal("refresh exposed unverified payment link")
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
	u := update(6, "")
	u.CallbackData = button(t, m, "确认下单")
	store.fail = true
	if _, e := s.Handle(context.Background(), 1, u); !errors.Is(e, rt.ErrConflict) {
		t.Fatal(e)
	}
	run(t, s, u)
	if len(p.keys) != 2 || p.keys[0] == "" || p.keys[0] != p.keys[1] {
		t.Fatalf("unstable payment operation: %+v", p.keys)
	}
}
