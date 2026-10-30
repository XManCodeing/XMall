package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type SessionStore interface {
	GetSession(context.Context, uint, int64, int64) (*rd.Session, error)
	SaveSession(context.Context, *rd.Session, uint64) error
	Contact(context.Context, uint, int64) (*rd.Contact, error)
	SaveContact(context.Context, *rd.Contact) error
}
type Dependencies struct {
	Content  c.ContentProvider
	Store    SessionStore
	Identity c.Identity
	Catalog  c.Catalog
	Orders   c.Orders
	Payments c.Payments
	Accounts c.Accounts
	Secret   string
}
type Service struct {
	d   Dependencies
	key []byte
	now func() time.Time
}

func New(d Dependencies) *Service {
	return &Service{d: d, key: crypto.DeriveKey(d.Secret), now: time.Now}
}

var _ rt.Handler = (*Service)(nil)

type action struct {
	Kind      string
	ID, Owner uint
	Page      int
	Value     string
}
type state struct {
	Generation                          string
	UserID                              uint
	Locale, Step, Ref, Operation, Email string
	Selection                           c.Selection
	Quote                               *c.Quote
	Actions                             map[string]action
	LastUpdate                          int64
	ReplyOwner                          uint
	Reply                               []rt.Message
	PanelEpoch                          string
	PanelRevision                       uint64
	PanelTarget                         int64
	Current, ReturnPage                 action
	Category                            uint
	ProductPage                         int
	Filter                              string
	MoreQuantity                        bool
	OrderID, OrderOwner                 uint
	Payment                             *c.Payment
	PaymentUncertain                    bool
	UncertainOrders                     map[uint]bool
	Resent                              map[uint]time.Time
}
type view struct {
	service  *Service
	session  *rd.Session
	state    *state
	actor    c.Actor
	update   rt.Update
	content  c.Content
	messages []rt.Message
	replace  bool
}

func (s *Service) Handle(ctx context.Context, botID uint, u rt.Update) ([]rt.Message, error) {
	if !u.Private || u.UserID <= 0 || u.ChatID != u.UserID || botID == 0 {
		return nil, nil
	}
	a := c.Actor{BotID: botID, TelegramID: u.UserID, Username: u.Username, Locale: locale(u.Locale)}
	uid, err := s.d.Identity.Resolve(ctx, a, true)
	if err != nil {
		return nil, err
	}
	if uid == 0 {
		return nil, c.ErrDenied
	}
	a.UserID = uid
	if identity, ok := s.d.Identity.(c.IdentityGeneration); ok {
		a.Generation, err = identity.Generation(ctx, a)
		if err != nil {
			return nil, err
		}
	}
	row, err := s.d.Store.GetSession(ctx, botID, u.UserID, u.ChatID)
	if err != nil && !errors.Is(err, rt.ErrNotFound) {
		return nil, err
	}
	st := &state{Locale: a.Locale, UserID: uid, Generation: a.Generation}
	if row == nil {
		row = &rd.Session{BotID: botID, TelegramUserID: u.UserID, ChatID: u.ChatID}
	} else if row.ExpiresAt.After(s.now()) && row.DataCipher != "" {
		plain, e := crypto.Decrypt(s.key, row.DataCipher)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(plain), st); e != nil {
			return nil, e
		}
	}
	if st.UserID != uid || st.Generation != a.Generation {
		st = &state{Locale: st.Locale, UserID: uid, Generation: a.Generation}
	}
	a.Locale = locale(st.Locale)
	contact, err := s.d.Store.Contact(ctx, botID, u.UserID)
	if err != nil && !errors.Is(err, rt.ErrNotFound) {
		return nil, err
	}
	if contact == nil {
		contact = &rd.Contact{BotID: botID, TelegramUserID: u.UserID}
	}
	contact.ChatID = u.ChatID
	contact.UserID = uid
	contact.Reachable = true
	contact.Locale = a.Locale
	if err = s.d.Store.SaveContact(ctx, contact); err != nil {
		return nil, err
	}
	if u.ID != 0 && st.LastUpdate == u.ID {
		if st.ReplyOwner != 0 {
			if e := s.AuthorizeDelivery(ctx, botID, u.UserID, st.ReplyOwner); e != nil {
				return nil, e
			}
		}
		return st.Reply, nil
	}
	// Inbox is ordered. An older update must never repeat a quantity delta or a write.
	if u.ID != 0 && st.LastUpdate > u.ID {
		return nil, nil
	}
	v := &view{service: s, session: row, state: st, actor: a, update: u}
	if s.d.Content != nil {
		v.content, err = s.d.Content.Content(ctx, a)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(st.Locale) == "" && strings.TrimSpace(v.content.DefaultLocale) != "" {
			st.Locale = locale(v.content.DefaultLocale)
			a.Locale = st.Locale
			v.actor.Locale = st.Locale
			v.content, err = s.d.Content.Content(ctx, a)
			if err != nil {
				return nil, err
			}
		}
	}
	var act action
	stale := false
	if row.PanelMessageID > 0 && row.PanelMessageEpoch == st.PanelEpoch {
		st.PanelTarget = row.PanelMessageID
	}
	if u.CallbackData != "" {
		parts := strings.Split(u.CallbackData, ":")
		if len(parts) != 4 || parts[0] != "s" || parts[1] != st.Ref {
			stale = true
		} else {
			var ok bool
			act, ok = st.Actions[parts[3]]
			stale = !ok || (parts[2] != strconv.FormatUint(st.PanelRevision, 36) && !(act.Kind == "delta" && parts[2] == "0"))
		}
	}
	if st.PanelTarget > 0 && u.CallbackData != "" && u.MessageID > 0 && st.PanelTarget != u.MessageID {
		stale = true
	}
	if st.PanelEpoch == "" {
		st.PanelEpoch, err = nonce()
		if err != nil {
			return nil, err
		}
	}
	if stale {
		if st.PanelTarget == 0 && row.PanelMessageID > 0 && row.PanelMessageID == u.MessageID {
			st.PanelTarget = u.MessageID
		}
		rt.AnswerCallback(ctx, text(a.Locale, "restored"))
		// A callback from an older physical panel cannot take over the active target.
		if st.PanelTarget != 0 && u.MessageID != 0 && st.PanelTarget != u.MessageID {
			return []rt.Message{{Purpose: "cleanup", Operation: "delete", TargetMessageID: u.MessageID, ChatID: u.ChatID, OwnerUserID: uid, IdentityStamp: a.Generation}}, nil
		}
		act = st.Current
		if act.Kind == "" {
			act = action{Kind: "home"}
		}
		// Recovery is read-only: do not retry payment creation, confirmation or cancellation.
		if st.OrderID != 0 && (act.Kind == "channels" || act.Kind == "pay" || act.Kind == "cancel_confirm") {
			act = action{Kind: "order", ID: st.OrderID, Owner: st.OrderOwner}
		}
	}
	if u.CallbackData != "" && !stale && u.MessageID > 0 {
		st.PanelTarget = u.MessageID
	}
	if u.CallbackData == "" && strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		st.PanelEpoch, err = nonce()
		if err != nil {
			return nil, err
		}
		v.replace = true
		st.PanelTarget = 0
	}
	// Ref is the operation context, not a render or CAS version. Keep it for deltas.
	if st.Ref == "" || (!stale && u.CallbackData != "" && act.Kind != "delta" && act.Kind != "quantity" && act.Kind != "more_quantity" && act.Kind != "quantity_input" && act.Kind != "details" && act.Kind != "purchase" && act.Kind != "noop" && act.Kind != "label" && act.Kind != "check_payment") || v.replace {
		st.Ref, err = nonce()
		if err != nil {
			return nil, err
		}
	}
	oldActions := st.Actions
	st.Actions = map[string]action{}
	st.ReplyOwner = 0
	if stale || u.CallbackData != "" {
		err = v.perform(ctx, act)
	} else {
		err = v.input(ctx, strings.TrimSpace(u.Text))
	}
	if err != nil {
		if !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrUnavailable) && !errors.Is(err, c.ErrPriceChanged) {
			return nil, err
		}
		v.say(text(a.Locale, "failed"))
		v.homeButton()
	}
	rt.AnswerCallback(ctx, "")
	// Keep user commands as conversation anchors; only transient text inputs
	// (quantity, email, verification code, etc.) are eligible for cleanup.
	if u.CallbackData == "" && u.MessageID > 0 && !strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		v.messages = append([]rt.Message{{Purpose: "cleanup", Operation: "delete", TargetMessageID: u.MessageID, ChatID: u.ChatID}}, v.messages...)
	}
	st.LastUpdate = u.ID
	if len(v.messages) == 0 {
		st.Actions = oldActions
	}
	return v.save(ctx)
}
func (v *view) save(ctx context.Context) ([]rt.Message, error) {
	for _, message := range v.messages {
		if message.Purpose == "panel" {
			v.state.PanelRevision++
			break
		}
	}
	v.session.PanelEpoch = v.state.PanelEpoch
	v.session.PanelRevision = v.state.PanelRevision
	for i := range v.messages {
		m := &v.messages[i]
		m.IdentityStamp = v.actor.Generation
		m.OwnerUserID = v.actor.UserID
		if m.Purpose != "cleanup" && v.state.ReplyOwner != 0 {
			m.OwnerUserID = v.state.ReplyOwner
		}
		if m.Purpose == "panel" {
			budget := 3900
			if len(m.Photo) > 0 || m.PhotoURL != "" || m.FileID != "" {
				budget = 950
			}
			m.Text = limitHTML(m.Text, budget)
			m.PanelEpoch = v.state.PanelEpoch
			m.PanelRevision = v.state.PanelRevision
			m.ReplacePanel = v.replace
			m.TargetMessageID = v.state.PanelTarget
		}
	}
	v.state.Reply = v.messages
	raw, e := json.Marshal(v.state)
	if e != nil {
		return nil, e
	}
	cipher, e := crypto.Encrypt(v.service.key, string(raw))
	if e != nil {
		return nil, e
	}
	v.session.DataCipher = cipher
	v.session.State = v.state.Step
	v.session.ExpiresAt = v.service.now().Add(30 * time.Minute)
	if e = v.service.d.Store.SaveSession(ctx, v.session, v.session.Version); e != nil {
		return nil, e
	}
	return v.messages, nil
}
func (s *Service) DeliveryStamp(ctx context.Context, bot uint, tg int64) (string, error) {
	a := c.Actor{BotID: bot, TelegramID: tg}
	uid, e := s.d.Identity.Resolve(ctx, a, false)
	if e != nil {
		return "", e
	}
	if uid == 0 {
		return "", c.ErrDenied
	}
	a.UserID = uid
	if identity, ok := s.d.Identity.(c.IdentityGeneration); ok {
		return identity.Generation(ctx, a)
	}
	return "", nil
}
func (s *Service) AuthorizeDelivery(ctx context.Context, botID uint, telegramID int64, userID uint) error {
	if userID == 0 || telegramID <= 0 {
		return c.ErrDenied
	}
	contact, e := s.d.Store.Contact(ctx, botID, telegramID)
	if e != nil {
		return e
	}
	if contact == nil || !contact.Reachable || contact.ChatID != telegramID {
		return c.ErrDenied
	}
	a := c.Actor{BotID: botID, TelegramID: telegramID}
	current, e := s.d.Identity.Resolve(ctx, a, false)
	if e != nil {
		return e
	}
	if current == 0 {
		return c.ErrDenied
	}
	a.UserID = current
	if current == userID {
		return nil
	}
	ids, e := s.d.Identity.HistoryUsers(ctx, a)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if id == userID {
			return nil
		}
	}
	return c.ErrDenied
}
func nonce() (string, error) {
	b := make([]byte, 12)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func (v *view) allowed(ctx context.Context, owner uint) error {
	if owner == v.actor.UserID {
		return nil
	}
	ids, e := v.service.d.Identity.HistoryUsers(ctx, v.actor)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if id == owner {
			return nil
		}
	}
	return c.ErrDenied
}

func (v *view) input(ctx context.Context, t string) error {
	parts := strings.Fields(t)
	cmd := ""
	if len(parts) > 0 {
		cmd = strings.Split(parts[0], "@")[0]
	}
	switch cmd {
	case "/start", "/cancel", "/back":
		return v.home(ctx)
	case "/help":
		return v.perform(ctx, action{Kind: "help"})
	case "/language":
		return v.perform(ctx, action{Kind: "language"})
	}
	switch v.state.Step {
	case "quantity":
		n, e := strconv.Atoi(t)
		if e != nil || n < 1 || n > 10000 {
			v.say(text(v.actor.Locale, "quantity"))
			return v.purchase(ctx, false)
		}
		return v.changeQuantity(ctx, n)
	case "email":
		if len(t) > 254 || !strings.Contains(t, "@") {
			v.say(text(v.actor.Locale, "email"))
			v.homeButton()
			return nil
		}
		if e := v.service.d.Identity.SendBindCode(ctx, v.actor, t); e != nil {
			return e
		}
		v.state.Email = t
		v.state.Step = "code"
		return v.bindCode("")
	case "code":
		if len(t) < 4 || len(t) > 12 {
			return v.bindCode(text(v.actor.Locale, "code_error"))
		}
		id, e := v.service.d.Identity.Bind(ctx, v.actor, v.state.Email, t)
		if e != nil || id == 0 {
			return v.bindCode(text(v.actor.Locale, "code_error"))
		}
		v.state.UserID = id
		v.actor.UserID = id
		if identity, ok := v.service.d.Identity.(c.IdentityGeneration); ok {
			generation, e := identity.Generation(ctx, v.actor)
			if e != nil {
				return e
			}
			v.actor.Generation = generation
			v.state.Generation = generation
		}
		v.state.Ref = ""
		var err error
		v.state.Ref, err = nonce()
		if err != nil {
			return err
		}
		return v.home(ctx)
	default:
		v.say(text(v.actor.Locale, "use_buttons"))
		return v.renderCurrent(ctx)
	}
}
func (v *view) bindCode(notice string) error {
	v.state.Current = action{Kind: "bind_code"}
	if notice != "" {
		v.say(notice)
	}
	v.say(text(v.actor.Locale, "code") + "\n" + v.state.Email)
	v.button(text(v.actor.Locale, "resend_code"), action{Kind: "resend_code"})
	v.homeButton()
	return nil
}
func (v *view) renderCurrent(ctx context.Context) error {
	a := v.state.Current
	if a.Kind == "" {
		a.Kind = "home"
	}
	if a.Kind == "pay" || a.Kind == "channels" {
		return v.order(ctx, v.state.OrderID, v.state.OrderOwner)
	}
	return v.perform(ctx, a)
}
func (v *view) perform(ctx context.Context, a action) error {
	switch a.Kind {
	case "language", "locale", "bind_code", "resend_code", "quantity_input", "purchase", "noop", "details", "more_quantity", "delta", "quantity", "sku":
	default:
		v.state.Step = ""
	}
	switch a.Kind {
	case "label":
		rt.AnswerCallback(ctx, a.Value)
		return nil
	case "home":
		return v.home(ctx)
	case "catalog", "products":
		return v.catalog(ctx, a)
	case "product", "sku", "delta", "quantity", "more_quantity", "quantity_input", "details", "purchase", "noop":
		return v.catalogAction(ctx, a)
	case "confirm":
		return v.confirm(ctx)
	case "orders", "history_orders":
		v.state.Filter = a.Value
		v.state.Current = a
		return v.orders(ctx, chooseOwner(a.Owner, v.actor.UserID), a.Page)
	case "order":
		return v.order(ctx, a.ID, a.Owner)
	case "channels":
		return v.channels(ctx, a.ID, false)
	case "pay":
		return v.pay(ctx, a)
	case "cancel_order", "cancel_confirm", "rebuy", "resend_delivery", "check_payment":
		return v.orderAction(ctx, a)
	case "help":
		return v.help(a)
	case "language":
		v.state.ReturnPage = v.state.Current
		v.state.Current = a
		v.html("<b>" + html.EscapeString(text(v.actor.Locale, "language")) + "</b>")
		names := map[string]string{"zh-CN": "简体中文", "zh-TW": "繁體中文", "en-US": "English"}
		for _, l := range []string{"zh-CN", "zh-TW", "en-US"} {
			if locale(v.actor.Locale) == l {
				v.styled("✓ "+names[l], stylePrimary, action{Kind: "locale", Value: l})
				continue
			}
			v.button(names[l], action{Kind: "locale", Value: l})
		}
		v.columns(3, 0)
		v.homeButton()
	case "locale":
		v.state.Locale = locale(a.Value)
		v.actor.Locale = v.state.Locale
		if v.service.d.Content != nil {
			content, e := v.service.d.Content.Content(ctx, v.actor)
			if e != nil {
				return e
			}
			v.content = content
		}
		target := v.state.ReturnPage
		if target.Kind == "" || target.Kind == "language" {
			target.Kind = "home"
		}
		if target.Kind == "pay" || target.Kind == "channels" {
			target = action{Kind: "order", ID: v.state.OrderID, Owner: v.state.OrderOwner}
		}
		return v.perform(ctx, target)
	case "account":
		acc, e := v.service.d.Accounts.Account(ctx, v.actor)
		if e != nil {
			return e
		}
		if acc == nil || acc.ID != v.actor.UserID {
			return c.ErrDenied
		}
		v.state.Current = a
		v.html("<b>" + html.EscapeString(text(v.actor.Locale, "account")) + "</b>")
		facts := []string{html.EscapeString(fmt.Sprintf(text(v.actor.Locale, "account_id"), acc.ID))}
		if acc.Email != "" {
			facts = append(facts, html.EscapeString(text(v.actor.Locale, "email_label")+acc.Email))
		}
		facts = append(facts, html.EscapeString(text(v.actor.Locale, "balance_label"))+"<b>"+html.EscapeString(money(acc.Currency, acc.Balance))+"</b>")
		v.html(strings.Join(facts, "\n"))
		start := len(v.panel().Buttons)
		v.button(text(v.actor.Locale, "bind"), action{Kind: "bind"})
		v.button(text(v.actor.Locale, "history"), action{Kind: "history"})
		v.columns(2, start)
		v.homeButton()
	case "bind":
		v.state.Current = a
		v.state.Step = "email"
		v.say(text(v.actor.Locale, "email"))
		v.homeButton()
	case "bind_code":
		return v.bindCode("")
	case "resend_code":
		if v.state.Step != "code" || v.state.Email == "" {
			return c.ErrDenied
		}
		if e := v.service.d.Identity.SendBindCode(ctx, v.actor, v.state.Email); e != nil {
			return e
		}
		return v.bindCode("")
	case "history", "history_account", "history_refunds":
		return v.history(ctx, a)
	default:
		return c.ErrDenied
	}
	return nil
}
func chooseOwner(owner, current uint) uint {
	if owner == 0 {
		return current
	}
	return owner
}
