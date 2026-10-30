package application

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"image/png"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/XManCodeing/XMall/internal/constants"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/shopspring/decimal"
)

// PaymentMedia performs no network I/O. QRImage is fetched and validated by
// the infrastructure adapter; raw QR payloads are encoded locally.
func PaymentMedia(p c.Payment) ([]byte, string, error) {
	link := ""
	u, e := url.Parse(strings.TrimSpace(p.PayURL))
	if e == nil && u.User == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		link = u.String()
	}
	if p.InteractionMode == constants.PaymentInteractionRedirect {
		if link == "" {
			return nil, "", c.ErrUnavailable
		}
		return nil, link, nil
	}
	if len(p.QRImage) > 0 {
		cfg, err := png.DecodeConfig(bytes.NewReader(p.QRImage))
		if err != nil || len(p.QRImage) > 2*1024*1024 || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, link, c.ErrUnavailable
		}
		return p.QRImage, link, nil
	}
	raw := strings.TrimSpace(p.QRCode)
	if p.InteractionMode == constants.PaymentInteractionQR && raw == "" {
		// Some gateways put their raw QR payload in PayURL rather than QRCode.
		raw = strings.TrimSpace(p.PayURL)
	}
	if p.QRCodeKind == "image" && !strings.HasPrefix(raw, "data:image/png;base64,") && len(p.QRImage) == 0 {
		return nil, link, c.ErrUnavailable
	}
	if strings.HasPrefix(raw, "data:image/png;base64,") {
		if len(raw) > 2*1024*1024 {
			return nil, link, c.ErrUnavailable
		}
		b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "data:image/png;base64,"))
		if e != nil {
			return nil, link, c.ErrUnavailable
		}
		cfg, e := png.DecodeConfig(bytes.NewReader(b))
		if e != nil || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, link, c.ErrUnavailable
		}
		return b, link, nil
	}
	if raw == "" && link == "" {
		raw = strings.TrimSpace(p.PayURL)
	}
	if raw == "" {
		return nil, link, nil
	}
	parsed, e := url.Parse(raw)
	if len(raw) > 2048 {
		return nil, link, c.ErrUnavailable
	}
	// Only known payment URI schemes are accepted when PayURL contains URLScheme.
	if p.QRCode == "" && p.InteractionMode != constants.PaymentInteractionQR {
		if e != nil || parsed == nil || (parsed.Scheme != "alipays" && parsed.Scheme != "alipay" && parsed.Scheme != "weixin") {
			return nil, link, c.ErrUnavailable
		}
	}
	code, e := qr.Encode(raw, qr.M, qr.Auto)
	if e != nil {
		return nil, link, e
	}
	img, e := barcode.Scale(code, 320, 320)
	if e != nil {
		return nil, link, e
	}
	var b bytes.Buffer
	if e = png.Encode(&b, img); e != nil {
		return nil, link, e
	}
	return b.Bytes(), link, nil
}
func (v *view) payment(p *c.Payment, orderID uint) error {
	return v.paymentPanel(p, orderID, nil)
}
func (v *view) paymentPanel(p *c.Payment, orderID uint, items []c.OrderItem) error {
	if p == nil {
		return c.ErrUnavailable
	}
	v.state.Current = action{Kind: "pay", ID: orderID}
	v.state.OrderID = orderID
	v.state.OrderOwner = v.actor.UserID
	if p.Paid {
		v.html("<b>" + html.EscapeString(p.OrderNumber) + "</b>")
		v.say(text(v.actor.Locale, "paid"))
	} else {
		if p.ExpiresAt != nil && !p.ExpiresAt.After(v.service.now()) {
			return c.ErrUnavailable
		}
		photo, link, e := PaymentMedia(*p)
		if e != nil {
			return e
		}
		if len(photo) == 0 && link == "" {
			return c.ErrUnavailable
		}
		v.html("<b>" + html.EscapeString(text(v.actor.Locale, "order_created_pay")) + "</b>")
		if lines := itemLines(items); lines != "" {
			v.say(lines)
		}
		facts := []string{}
		if p.OrderNumber != "" {
			facts = append(facts, html.EscapeString(text(v.actor.Locale, "order_no")+p.OrderNumber))
		}
		facts = append(facts, html.EscapeString(text(v.actor.Locale, "payable"))+" <b>"+html.EscapeString(money(p.Currency, p.Amount))+"</b>")
		if p.ExpiresAt != nil {
			facts = append(facts, html.EscapeString(fmt.Sprintf(text(v.actor.Locale, "pay_before"), v.clock(*p.ExpiresAt))))
		}
		v.html(strings.Join(facts, "\n"))
		v.say(text(v.actor.Locale, "payment_wait"))
		v.panel().Photo = photo
		if link != "" {
			v.panel().Buttons = append(v.panel().Buttons, []rt.Button{{Text: text(v.actor.Locale, "pay"), URL: link, Style: stylePrimary}})
		}
	}
	v.styled(text(v.actor.Locale, "refresh"), styleSuccess, action{Kind: "check_payment", ID: orderID, Owner: v.actor.UserID})
	return nil
}
func itemLines(items []c.OrderItem) string {
	lines := []string{}
	for _, item := range items {
		name := item.Title
		if item.SKUName != "" {
			name += " · " + item.SKUName
		}
		lines = append(lines, fmt.Sprintf("%s × %d", name, item.Quantity))
	}
	return strings.Join(lines, "\n")
}
func (v *view) currentOrder(ctx context.Context, id uint) (*c.Order, error) {
	o, e := v.service.d.Orders.Detail(ctx, v.actor, id, v.actor.UserID)
	if e != nil {
		return nil, e
	}
	if o == nil || o.UserID != v.actor.UserID {
		return nil, c.ErrDenied
	}
	return o, nil
}
func (v *view) channels(ctx context.Context, id uint, automatic bool) error {
	o, e := v.currentOrder(ctx, id)
	if e != nil {
		return e
	}
	if !o.Pending || (o.ExpiresAt != nil && !o.ExpiresAt.After(v.service.now())) {
		return v.order(ctx, id, v.actor.UserID)
	}
	v.state.Current = action{Kind: "channels", ID: id}
	v.state.OrderID = id
	v.state.OrderOwner = v.actor.UserID
	v.state.PaymentUncertain = v.state.UncertainOrders[id]
	if v.state.PaymentUncertain {
		v.say(text(v.actor.Locale, "payment_uncertain"))
		v.styled(text(v.actor.Locale, "refresh"), styleSuccess, action{Kind: "check_payment", ID: id, Owner: v.actor.UserID})
		v.homeButton()
		return nil
	}
	chs, e := v.service.d.Payments.Channels(ctx, v.actor, id)
	if e != nil {
		return e
	}
	external := []c.Channel{}
	for _, ch := range chs {
		if !ch.Wallet {
			external = append(external, ch)
		}
	}
	if automatic && len(external) == 1 {
		return v.pay(ctx, action{Kind: "pay", ID: id, Owner: external[0].ID, Value: v.paymentKey(id, external[0].ID)})
	}
	if automatic {
		v.html("<b>" + html.EscapeString(text(v.actor.Locale, "order_created")) + "</b>")
	}
	v.orderHeading(o)
	v.say(text(v.actor.Locale, "channels"))
	v.paymentButtons(ctx, o, chs)
	v.footer(id)
	return nil
}

// footer puts the destructive action next to home so it never reads as primary.
func (v *view) footer(id uint) {
	start := len(v.panel().Buttons)
	v.cancelButton(id)
	v.homeButton()
	v.columns(2, start)
}
func (v *view) paymentKey(id, ch uint) string {
	// Derive from persisted checkout operation (or context for existing orders),
	// never from fresh randomness after a side effect. CAS retries reuse this key.
	op := v.state.Operation
	if op == "" {
		op = fmt.Sprintf("order:%d", id)
	}
	return fmt.Sprintf("tgp:%d:%d:%d:%d:%s", v.actor.BotID, v.actor.UserID, id, ch, op)
}
func (v *view) paymentButtons(ctx context.Context, o *c.Order, chs []c.Channel) {
	start := len(v.panel().Buttons)
	for _, ch := range chs {
		if !ch.Wallet {
			v.button(ch.Name, action{Kind: "pay", ID: o.ID, Owner: ch.ID, Value: v.paymentKey(o.ID, ch.ID)})
		}
	}
	v.columns(2, start)
	for _, ch := range chs {
		if ch.Wallet {
			acc, e := v.service.d.Accounts.Account(ctx, v.actor)
			if e != nil || acc == nil || acc.ID != v.actor.UserID {
				continue
			}
			balance, be := decimal.NewFromString(acc.Balance)
			amount, ae := decimal.NewFromString(o.Amount)
			if be == nil && ae == nil && balance.LessThan(amount) {
				continue
			}
			v.button(fmt.Sprintf(text(v.actor.Locale, "wallet_pay"), money(o.Currency, o.Amount), money(acc.Currency, acc.Balance)), action{Kind: "pay", ID: o.ID, Owner: ch.ID, Value: v.paymentKey(o.ID, ch.ID)})
		}
	}
}
func (v *view) pay(ctx context.Context, a action) error {
	o, e := v.currentOrder(ctx, a.ID)
	if e != nil {
		return e
	}
	if !o.Pending || (o.ExpiresAt != nil && !o.ExpiresAt.After(v.service.now())) {
		return v.order(ctx, a.ID, v.actor.UserID)
	}
	if v.state.UncertainOrders[a.ID] {
		return v.channels(ctx, a.ID, false)
	}
	chs, e := v.service.d.Payments.Channels(ctx, v.actor, a.ID)
	if e != nil {
		return e
	}
	valid := false
	for _, ch := range chs {
		if ch.ID == a.Owner {
			valid = true
		}
	}
	if !valid {
		return c.ErrDenied
	}
	rt.AnswerCallback(ctx, text(v.actor.Locale, "processing"))
	p, e := v.service.d.Payments.Pay(ctx, v.actor, a.ID, a.Owner, a.Value)
	v.state.Current = action{Kind: "pay", ID: a.ID}
	v.state.OrderID = a.ID
	v.state.OrderOwner = v.actor.UserID
	if errors.Is(e, c.ErrPaymentUncertain) {
		v.state.Payment = nil
		v.state.PaymentUncertain = true
		if v.state.UncertainOrders == nil {
			v.state.UncertainOrders = map[uint]bool{}
		}
		v.state.UncertainOrders[a.ID] = true
		v.say(text(v.actor.Locale, "payment_uncertain"))
		v.styled(text(v.actor.Locale, "refresh"), styleSuccess, action{Kind: "check_payment", ID: a.ID, Owner: v.actor.UserID})
		v.homeButton()
		return nil
	}
	if e != nil {
		return e
	}
	if p == nil {
		return c.ErrUnavailable
	}
	if p.OrderNumber == "" {
		p.OrderNumber = o.Number
	}
	if p.Amount == "" {
		p.Amount = o.Amount
	}
	if p.Currency == "" {
		p.Currency = o.Currency
	}
	if p.ExpiresAt == nil {
		p.ExpiresAt = o.ExpiresAt
	}
	v.state.Payment = p
	if e = v.paymentPanel(p, a.ID, o.Items); e != nil {
		return e
	}
	if !p.Paid {
		wallet := []c.Channel{}
		for _, ch := range chs {
			if ch.Wallet {
				wallet = append(wallet, ch)
			}
		}
		v.paymentButtons(ctx, o, wallet)
		if len(chs) > 1 {
			v.button(text(v.actor.Locale, "change_payment"), action{Kind: "channels", ID: a.ID})
		}
		v.footer(a.ID)
		return nil
	}
	v.homeButton()
	return nil
}
func (v *view) cancelButton(id uint) {
	if _, ok := v.service.d.Orders.(c.OrdersCancel); ok {
		v.styled(text(v.actor.Locale, "cancel_order"), styleDanger, action{Kind: "cancel_order", ID: id})
	}
}
func (v *view) orderAction(ctx context.Context, a action) error {
	if a.Kind == "resend_delivery" {
		if e := v.allowed(ctx, a.Owner); e != nil {
			return e
		}
		o, e := v.service.d.Orders.Detail(ctx, v.actor, a.ID, a.Owner)
		if e != nil {
			return e
		}
		if o == nil || o.UserID != a.Owner {
			return c.ErrDenied
		}
		if v.state.Resent == nil {
			v.state.Resent = map[uint]time.Time{}
		}
		if previous, ok := v.state.Resent[a.ID]; ok && v.service.now().Sub(previous) < 30*time.Second {
			rt.AnswerCallback(ctx, text(v.actor.Locale, "delivery_recent"))
			return v.order(ctx, a.ID, a.Owner)
		}
		for _, delivery := range o.Delivered {
			if delivery != "" {
				v.messages = append(v.messages, rt.Message{Purpose: "delivery", Operation: "send", ChatID: v.update.ChatID, Text: delivery, ForceDocument: len(utf16.Encode([]rune(delivery))) > 3500, MediaName: "delivery.txt"})
			}
		}
		v.state.Resent[a.ID] = v.service.now()
		return v.order(ctx, a.ID, a.Owner)
	}
	o, e := v.currentOrder(ctx, a.ID)
	if e != nil {
		return e
	}
	switch a.Kind {
	case "check_payment":
		if o.Pending && (o.ExpiresAt == nil || o.ExpiresAt.After(v.service.now())) {
			rt.AnswerCallback(ctx, text(v.actor.Locale, "not_paid"))
			return nil
		}
		return v.order(ctx, a.ID, v.actor.UserID)
	case "cancel_order":
		if !o.Pending {
			return v.order(ctx, a.ID, v.actor.UserID)
		}
		if _, ok := v.service.d.Orders.(c.OrdersCancel); !ok {
			return c.ErrUnavailable
		}
		v.state.Current = action{Kind: "order", ID: a.ID, Owner: v.actor.UserID}
		v.html("<b>" + html.EscapeString(text(v.actor.Locale, "cancel_title")) + "</b>")
		v.say(text(v.actor.Locale, "cancel_warning"))
		v.button(text(v.actor.Locale, "keep_order"), action{Kind: "order", ID: a.ID, Owner: v.actor.UserID})
		v.styled(text(v.actor.Locale, "cancel_confirm"), styleDanger, action{Kind: "cancel_confirm", ID: a.ID})
		v.columns(2, 0)
		return nil
	case "cancel_confirm":
		if !o.Pending {
			return v.order(ctx, a.ID, v.actor.UserID)
		}
		cancel, ok := v.service.d.Orders.(c.OrdersCancel)
		if !ok {
			return c.ErrUnavailable
		}
		rt.AnswerCallback(ctx, text(v.actor.Locale, "processing"))
		if e = cancel.Cancel(ctx, v.actor, a.ID); e != nil {
			return e
		}
		v.state.Payment = nil
		v.state.PaymentUncertain = false
		return v.order(ctx, a.ID, v.actor.UserID)
	case "rebuy":
		if o.Pending || !(o.Status == "cancelled" || o.Status == "canceled" || o.Status == "expired") || len(o.Items) != 1 {
			return c.ErrDenied
		}
		item := o.Items[0]
		p, e := v.service.d.Catalog.Product(ctx, v.actor, item.ProductID)
		if e != nil {
			return e
		}
		if p == nil {
			return c.ErrUnavailable
		}
		v.state.Selection = c.Selection{ProductID: item.ProductID, SKUID: item.SKUID, Quantity: item.Quantity}
		v.state.Quote = nil
		v.state.Operation = ""
		v.state.Payment = nil
		v.state.PaymentUncertain = false
		v.state.Step = ""
		return v.purchase(ctx, true)
	}
	return c.ErrDenied
}
