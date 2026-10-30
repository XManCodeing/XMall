package application

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"github.com/shopspring/decimal"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// say appends a paragraph to the one panel, never to a credential delivery.
func (v *view) panel() *rt.Message {
	for i := range v.messages {
		if v.messages[i].Purpose == "panel" {
			return &v.messages[i]
		}
	}
	v.messages = append(v.messages, rt.Message{ChatID: v.update.ChatID, Purpose: "panel", Operation: "edit", ParseMode: "HTML"})
	return &v.messages[len(v.messages)-1]
}
func (v *view) say(t string) { v.html(html.EscapeString(t)) }
func (v *view) html(t string) {
	m := v.panel()
	if m.Text != "" && t != "" {
		m.Text += "\n\n"
	}
	m.Text += t
}
func (v *view) button(label string, a action) {
	if v.state.Actions == nil {
		v.state.Actions = map[string]action{}
	}
	key := strconv.FormatInt(int64(len(v.state.Actions)), 36)
	// Stable delta keys allow all clicks from the same selection context to accumulate.
	version := strconv.FormatUint(v.state.PanelRevision+1, 36)
	if a.Kind == "delta" {
		key = "d" + a.Value
		version = "0"
	}
	v.state.Actions[key] = a
	m := v.panel()
	m.Buttons = append(m.Buttons, []rt.Button{{Text: label, Data: fmt.Sprintf("s:%s:%s:%s", v.state.Ref, version, key)}})
}

const (
	stylePrimary = "primary"
	styleSuccess = "success"
	styleDanger  = "danger"
)

func (v *view) styled(label, style string, a action) {
	v.button(label, a)
	v.styleLast(style)
}
func (v *view) styleLast(style string) {
	m := v.panel()
	if len(m.Buttons) == 0 {
		return
	}
	row := m.Buttons[len(m.Buttons)-1]
	row[len(row)-1].Style = style
}

// section renders a full-width, non-mutating heading row between button groups.
func (v *view) section(title, hint string) {
	v.button("— "+title+" —", action{Kind: "label", Value: hint})
}
func (v *view) columns(width, start int) {
	m := v.panel()
	if start >= len(m.Buttons) {
		return
	}
	var flat []rt.Button
	for _, row := range m.Buttons[start:] {
		flat = append(flat, row...)
	}
	m.Buttons = m.Buttons[:start]
	for len(flat) > 0 {
		n := min(width, len(flat))
		m.Buttons = append(m.Buttons, flat[:n])
		flat = flat[n:]
	}
}
func (v *view) urlButton(label, raw string) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return
	}
	m := v.panel()
	m.Buttons = append(m.Buttons, []rt.Button{{Text: label, URL: u.String()}})
}
func (v *view) homeButton() { v.button(text(v.actor.Locale, "back"), action{Kind: "home"}) }

// navRow keeps back + home on one bottom row.
func (v *view) navRow(backLabel string, back action) {
	start := len(v.panel().Buttons)
	v.button(backLabel, back)
	v.homeButton()
	v.columns(2, start)
}
func (v *view) pages(a action, more bool) {
	start := len(v.panel().Buttons)
	if a.Page > 1 {
		prev := a
		prev.Page--
		v.button(text(v.actor.Locale, "page_prev"), prev)
	}
	if more {
		a.Page++
		v.button(text(v.actor.Locale, "page_next"), a)
	}
	v.columns(2, start)
}
func money(currency, amount string) string {
	if amount == "" {
		amount = "0"
	}
	if n, e := decimal.NewFromString(amount); e == nil {
		amount = n.StringFixed(2)
	}
	symbol := map[string]string{"CNY": "¥", "USD": "$", "EUR": "€", "TWD": "NT$", "HKD": "HK$"}[currency]
	if symbol == "" {
		symbol = currency + " "
	}
	return symbol + amount
}
func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func (v *view) clock(t time.Time) string { return t.In(time.Local).Format("01-02 15:04") }
func (v *view) contentConfigured() bool {
	return v.content.Menu != nil || v.content.WelcomeEnabled || v.content.HelpEnabled ||
		v.content.Welcome != "" || v.content.Help != "" || len(v.content.HelpItems) > 0 ||
		v.content.CenterHint != "" || v.content.SupportURL != "" || v.content.SupportHint != ""
}
func (v *view) home(ctx context.Context) error {
	v.state.Current = action{Kind: "home"}
	v.state.Step = ""
	v.state.Quote = nil
	v.state.Operation = ""
	v.state.Email = ""
	v.state.OrderID = 0
	v.state.OrderOwner = 0
	v.state.Payment = nil
	v.state.PaymentUncertain = false
	if !v.contentConfigured() || v.content.WelcomeEnabled {
		welcome := v.content.Welcome
		if welcome == "" {
			welcome = text(v.actor.Locale, "welcome")
		}
		v.say(short(welcome, 2600))
	}
	if filter, ok := v.service.d.Orders.(c.OrdersFilter); ok {
		summary, e := filter.PendingSummary(ctx, v.actor)
		if e != nil {
			return e
		}
		if summary.Count > 0 && summary.Latest != nil && summary.Latest.UserID == v.actor.UserID {
			v.html("<b>" + html.EscapeString(fmt.Sprintf(text(v.actor.Locale, "pending_summary"), summary.Count, money(summary.Latest.Currency, summary.Latest.Amount))) + "</b>")
			a := action{Kind: "orders", Page: 1, Value: "pending_payment"}
			if summary.Count == 1 {
				a = action{Kind: "channels", ID: summary.Latest.ID}
			}
			v.styled(text(v.actor.Locale, "continue_pay"), styleSuccess, a)
		}
	}
	if v.panel().Text == "" {
		v.say(text(v.actor.Locale, "use_buttons"))
	}
	// The shop entry is the primary action: full width and highlighted; the
	// rest of the menu flows in two columns around it.
	start := len(v.panel().Buttons)
	primary := func(label string) {
		v.columns(2, start)
		v.styled(label, stylePrimary, action{Kind: "catalog"})
		start = len(v.panel().Buttons)
	}
	if v.content.Menu == nil {
		primary(text(v.actor.Locale, "catalog"))
		for _, k := range []string{"orders", "account", "language", "help"} {
			if k == "help" && v.contentConfigured() && !v.content.HelpEnabled {
				continue
			}
			v.button(text(v.actor.Locale, k), action{Kind: k})
		}
	} else {
		for _, item := range v.content.Menu {
			if item.URL != "" {
				v.urlButton(item.Label, item.URL)
				continue
			}
			switch item.Action {
			case "catalog", "orders", "account", "language", "help":
				if item.Action == "help" && !v.content.HelpEnabled {
					continue
				}
				label := item.Label
				if label == "" {
					label = text(v.actor.Locale, item.Action)
				}
				if item.Action == "catalog" {
					primary(label)
					continue
				}
				v.button(label, action{Kind: item.Action})
			}
		}
	}
	v.columns(2, start)
	return nil
}
func (v *view) help(a action) error {
	v.state.Current = action{Kind: "help"}
	if v.contentConfigured() && !v.content.HelpEnabled {
		v.say(text(v.actor.Locale, "help_closed"))
		v.homeButton()
		return nil
	}
	if key := strings.TrimSpace(a.Value); key != "" {
		for _, item := range v.content.HelpItems {
			if item.Key != key {
				continue
			}
			title := item.Title
			if title == "" {
				title = item.Summary
			}
			if title != "" {
				v.say(short(title, 200))
			}
			if item.Body != "" {
				v.say(short(item.Body, 3500))
			}
			if item.ShowSupport {
				if v.content.SupportURL != "" {
					v.urlButton(text(v.actor.Locale, "support"), v.content.SupportURL)
				} else if hint := strings.TrimSpace(v.content.SupportHint); hint != "" {
					v.say(short(hint, 500))
				}
			}
			v.button(text(v.actor.Locale, "back_list"), action{Kind: "help"})
			v.homeButton()
			return nil
		}
	}
	if len(v.content.HelpItems) > 0 {
		for _, block := range []string{v.content.HelpTitle, v.content.HelpIntro, v.content.CenterHint} {
			if block = strings.TrimSpace(block); block != "" {
				v.say(short(block, 800))
			}
		}
		for _, item := range v.content.HelpItems {
			label := item.Summary
			if label == "" {
				label = item.Title
			}
			if label == "" {
				continue
			}
			v.button(short(label, 64), action{Kind: "help", Value: item.Key})
		}
		v.homeButton()
		return nil
	}
	help := v.content.Help
	if help == "" {
		help = text(v.actor.Locale, "help_text")
	}
	v.say(help)
	if v.content.SupportURL != "" {
		v.urlButton(text(v.actor.Locale, "support"), v.content.SupportURL)
	}
	v.homeButton()
	return nil
}
func (v *view) history(ctx context.Context, a action) error {
	v.state.Current = a
	if a.Kind == "history" {
		ids, e := v.service.d.Identity.HistoryUsers(ctx, v.actor)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "history"))
		for _, id := range ids {
			v.button(fmt.Sprintf("#%d", id), action{Kind: "history_orders", Owner: id, Page: 1})
		}
		v.homeButton()
		return nil
	}
	if a.Owner == v.actor.UserID {
		return c.ErrDenied
	}
	if e := v.allowed(ctx, a.Owner); e != nil {
		return e
	}
	reader, ok := v.service.d.Accounts.(c.HistoryAccounts)
	if !ok {
		return c.ErrUnavailable
	}
	v.state.ReplyOwner = a.Owner
	if a.Kind == "history_account" {
		acc, e := reader.HistoryAccount(ctx, v.actor, a.Owner)
		if e != nil {
			return e
		}
		if acc == nil || acc.ID != a.Owner {
			return c.ErrDenied
		}
		v.say(fmt.Sprintf("%s\n#%d\n%s", text(v.actor.Locale, "history_account"), acc.ID, money(acc.Currency, acc.Balance)))
		v.button(text(v.actor.Locale, "history_refunds"), action{Kind: "history_refunds", Owner: a.Owner, Page: 1})
	} else {
		if a.Page < 1 {
			a.Page = 1
		}
		rows, more, e := reader.HistoryRefunds(ctx, v.actor, a.Owner, a.Page)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "history_refunds"))
		for _, r := range rows {
			v.say(v.clock(r.CreatedAt) + " · " + money(r.Currency, r.Amount) + "\n" + r.Reference)
		}
		v.pages(a, more)
	}
	v.homeButton()
	return nil
}

// limitHTML counts Telegram's visible UTF-16 units, not entity bytes. Only
// generated <b> tags are accepted; dynamic strings are already escaped.
func limitHTML(s string, budget int) string {
	visible := strings.ReplaceAll(strings.ReplaceAll(s, "<b>", ""), "</b>", "")
	visible = html.UnescapeString(visible)
	if len(utf16.Encode([]rune(visible))) <= budget {
		return s
	}
	var b strings.Builder
	units := 0
	for _, r := range visible {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > budget-1 {
			break
		}
		b.WriteRune(r)
		units += n
	}
	return html.EscapeString(b.String() + "…")
}

func status(l, s string) string {
	if _, ok := texts["status_"+s]; ok {
		return text(l, "status_"+s)
	}
	return text(l, "status_unknown")
}
func (v *view) orders(ctx context.Context, owner uint, page int) error {
	if e := v.allowed(ctx, owner); e != nil {
		return e
	}
	if page < 1 {
		page = 1
	}
	v.state.ReplyOwner = owner
	var items []c.Order
	var more bool
	var e error
	if filter, ok := v.service.d.Orders.(c.OrdersFilter); ok {
		items, more, e = filter.ListFiltered(ctx, v.actor, owner, page, v.state.Filter)
	} else {
		items, more, e = v.service.d.Orders.List(ctx, v.actor, owner, page)
	}
	if e != nil {
		return e
	}
	v.html("<b>" + html.EscapeString(text(v.actor.Locale, "orders")) + "</b>")
	kind := "orders"
	if owner != v.actor.UserID {
		kind = "history_orders"
	}
	start := len(v.panel().Buttons)
	for _, f := range []string{"", "pending_payment"} {
		key := "all"
		if f != "" {
			key = "status_pending_payment"
		}
		label := text(v.actor.Locale, key)
		if v.state.Filter == f {
			v.styled("✓ "+label, stylePrimary, action{Kind: kind, Owner: owner, Page: 1, Value: f})
			continue
		}
		v.button(label, action{Kind: kind, Owner: owner, Page: 1, Value: f})
	}
	v.columns(2, start)
	listed := 0
	for _, o := range items {
		if o.UserID != owner {
			return c.ErrDenied
		}
		if v.state.Filter != "" && (!o.Pending || (o.ExpiresAt != nil && !o.ExpiresAt.After(v.service.now()))) {
			continue
		}
		if listed == 0 {
			v.section(text(v.actor.Locale, "section_orders"), text(v.actor.Locale, "section_orders_hint"))
		}
		listed++
		number := o.Number
		if len([]rune(number)) > 12 {
			number = string([]rune(number)[len([]rune(number))-12:])
		}
		v.button(number+" · "+status(v.actor.Locale, o.Status)+" · "+money(o.Currency, o.Amount), action{Kind: "order", ID: o.ID, Owner: owner})
	}
	if listed == 0 {
		v.say(text(v.actor.Locale, "orders_empty"))
	}
	v.pages(action{Kind: kind, Owner: owner, Page: page, Value: v.state.Filter}, more)
	if owner != v.actor.UserID {
		if _, ok := v.service.d.Accounts.(c.HistoryAccounts); ok {
			v.button(text(v.actor.Locale, "history_account"), action{Kind: "history_account", Owner: owner})
		}
	}
	v.homeButton()
	return nil
}
func (v *view) orderHeading(o *c.Order) {
	v.html("<b>" + html.EscapeString(text(v.actor.Locale, "order_no")+o.Number) + "</b>")
	if lines := itemLines(o.Items); lines != "" {
		v.say(lines)
	}
	facts := []string{
		html.EscapeString(text(v.actor.Locale, "status_label") + status(v.actor.Locale, o.Status)),
		html.EscapeString(text(v.actor.Locale, "amount_label")) + "<b>" + html.EscapeString(money(o.Currency, o.Amount)) + "</b>",
	}
	if !o.CreatedAt.IsZero() {
		facts = append(facts, html.EscapeString(text(v.actor.Locale, "created_label")+v.clock(o.CreatedAt)))
	}
	if o.ExpiresAt != nil && o.Pending {
		facts = append(facts, html.EscapeString(text(v.actor.Locale, "expires")+v.clock(*o.ExpiresAt)))
	}
	v.html(strings.Join(facts, "\n"))
}
func (v *view) order(ctx context.Context, id, owner uint) error {
	if e := v.allowed(ctx, owner); e != nil {
		return e
	}
	o, e := v.service.d.Orders.Detail(ctx, v.actor, id, owner)
	if e != nil {
		return e
	}
	if o == nil || o.UserID != owner {
		return c.ErrDenied
	}
	v.state.Current = action{Kind: "order", ID: id, Owner: owner}
	v.state.OrderID = id
	v.state.OrderOwner = owner
	v.state.PaymentUncertain = v.state.UncertainOrders[id]
	v.state.ReplyOwner = owner
	v.state.Step = ""
	v.orderHeading(o)
	pending := owner == v.actor.UserID && o.Pending && (o.ExpiresAt == nil || o.ExpiresAt.After(v.service.now()))
	if pending && !v.state.PaymentUncertain {
		v.styled(text(v.actor.Locale, "go_pay"), stylePrimary, action{Kind: "channels", ID: id})
	} else if owner == v.actor.UserID && len(o.Items) == 1 && (o.Status == "cancelled" || o.Status == "canceled" || o.Status == "expired") {
		v.styled(text(v.actor.Locale, "rebuy"), stylePrimary, action{Kind: "rebuy", ID: id})
	}
	if len(o.Delivered) > 0 {
		v.button(text(v.actor.Locale, "resend_delivery"), action{Kind: "resend_delivery", ID: id, Owner: owner})
	}
	start := len(v.panel().Buttons)
	if pending && !v.state.PaymentUncertain {
		v.cancelButton(id)
	}
	v.button(text(v.actor.Locale, "refresh_order"), action{Kind: "order", ID: id, Owner: owner})
	v.columns(2, start)
	v.homeButton()
	return nil
}

// RefreshOrderPanel is an event hook. The CAS save fails rather than overwriting
// concurrent navigation; callers may drop ErrConflict and retain their notification.
func (s *Service) RefreshOrderPanel(ctx context.Context, bot uint, tg int64, orderID uint) ([]rt.Message, error) {
	row, e := s.d.Store.GetSession(ctx, bot, tg, tg)
	if e != nil {
		return nil, e
	}
	if row == nil || !row.ExpiresAt.After(s.now()) || row.DataCipher == "" {
		return nil, nil
	}
	plain, e := crypto.Decrypt(s.key, row.DataCipher)
	if e != nil {
		return nil, e
	}
	var st state
	if e = json.Unmarshal([]byte(plain), &st); e != nil {
		return nil, e
	}
	if st.OrderID != orderID || !(st.Current.Kind == "order" || st.Current.Kind == "channels" || st.Current.Kind == "pay") {
		return nil, nil
	}
	if e = s.AuthorizeDelivery(ctx, bot, tg, st.OrderOwner); e != nil {
		return nil, e
	}
	stamp, e := s.DeliveryStamp(ctx, bot, tg)
	if e != nil {
		return nil, e
	}
	if stamp != st.Generation {
		return nil, c.ErrDenied
	}
	uid, e := s.d.Identity.Resolve(ctx, c.Actor{BotID: bot, TelegramID: tg}, false)
	if e != nil {
		return nil, e
	}
	if uid != st.UserID {
		return nil, c.ErrDenied
	}
	v := &view{service: s, session: row, state: &st, actor: c.Actor{BotID: bot, TelegramID: tg, UserID: uid, Generation: stamp, Locale: locale(st.Locale)}, update: rt.Update{UserID: tg, ChatID: tg, Private: true}}
	st.Actions = map[string]action{}
	if row.PanelMessageID > 0 && row.PanelMessageEpoch == st.PanelEpoch {
		st.PanelTarget = row.PanelMessageID
	}
	st.Ref, e = nonce()
	if e != nil {
		return nil, e
	}
	if e = v.order(ctx, orderID, st.OrderOwner); e != nil {
		return nil, e
	}
	return v.save(ctx)
}
