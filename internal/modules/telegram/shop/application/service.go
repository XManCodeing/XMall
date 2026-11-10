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
	"sync"
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
	ProductLinks c.ProductLinkAccess
	Content      c.ContentProvider
	Store        SessionStore
	Identity     c.Identity
	Catalog      c.Catalog
	Orders       c.Orders
	Payments     c.Payments
	Accounts     c.Accounts
	Secret       string
}
type Service struct {
	panelScanMu  sync.Mutex
	panelCursors map[uint]uint
	membership   c.MembershipAccess
	d            Dependencies
	key          []byte
	now          func() time.Time
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
	MembershipPending                   *membershipPanelPending
	LinkProductID                       uint
	PublicGeneration                    string
	IdentityFailure                     string
	Generation                          string
	UserID                              uint
	Locale, Step, Ref, Operation, Email string
	Selection                           c.Selection
	Quote                               *c.Quote
	Actions                             map[string]action
	LastUpdate                          int64
	LegacyInputThrough                  int64
	ReplyOwner                          uint
	Reply                               []rt.Message
	PanelEpoch                          string
	PanelRevision                       uint64
	PanelTarget                         int64
	Pages                               []pageState
	ReturnView                          *pageState
	Current, ReturnPage                 action
	Category                            uint
	ProductPage                         int
	Filter                              string
	MoreQuantity                        bool
	OrderID, OrderOwner                 uint
	Payment                             *c.Payment
	PaymentUncertain                    bool
	UncertainOrders                     map[uint]bool
	PaymentKeys                         map[string]string // Authorized successor per order/channel checkout key.
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
	explicit bool
}

func (s *Service) Handle(ctx context.Context, botID uint, u rt.Update) ([]rt.Message, error) {
	return s.handle(ctx, botID, u, false)
}

// handle orchestrates validated session loading, callback routing and durable reply storage.
func (s *Service) handle(ctx context.Context, botID uint, u rt.Update, isolatedLegacy bool) ([]rt.Message, error) {
	if !u.Private || u.UserID <= 0 || u.ChatID != u.UserID || botID == 0 {
		return nil, nil
	}
	v, retired, err := s.loadView(ctx, botID, u, isolatedLegacy)
	if err != nil {
		return nil, err
	}
	contact, err := v.prepareContact(ctx)
	if err != nil {
		return nil, err
	}
	if u.ID != 0 && v.state.LastUpdate >= u.ID {
		return v.replay(ctx)
	}
	if s.d.Content != nil {
		v.content, err = s.d.Content.Content(ctx, v.actor)
		if err != nil {
			return nil, err
		}
	}
	act, stale, ignore, err := v.resolveAction(ctx)
	if err != nil || ignore {
		return nil, err
	}
	return v.executeUpdate(ctx, act, stale, isolatedLegacy, retired, contact)
}

// loadView loads identity without provisioning and retires expired or replaced session state.
func (s *Service) loadView(ctx context.Context, botID uint, u rt.Update, isolatedLegacy bool) (*view, bool, error) {
	a := c.Actor{BotID: botID, TelegramID: u.UserID, Username: u.Username, UsernamePresent: u.Actor != nil && !u.Actor.IsBot, Locale: supportedLocale(u.Locale)}
	uid, err := s.d.Identity.Resolve(ctx, a, false)
	failure := identityFailureKey(err)
	if err != nil && failure == "" {
		return nil, false, err
	}
	a.UserID = uid
	if identity, ok := s.d.Identity.(c.IdentityGeneration); ok && uid != 0 {
		a.Generation, err = identity.Generation(ctx, a)
		if err != nil {
			return nil, false, err
		}
	}
	row, err := s.d.Store.GetSession(ctx, botID, u.UserID, u.ChatID)
	if err != nil && !errors.Is(err, rt.ErrNotFound) {
		return nil, false, err
	}
	st := &state{UserID: uid, Generation: a.Generation}
	if row == nil {
		row = &rd.Session{BotID: botID, TelegramUserID: u.UserID, ChatID: u.ChatID}
	} else if row.DataCipher != "" {
		plain, err := crypto.Decrypt(s.key, row.DataCipher)
		if err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal([]byte(plain), st); err != nil {
			return nil, false, err
		}
	}
	retired := st.UserID != uid || st.Generation != a.Generation || !row.ExpiresAt.After(s.now())
	if retired {
		preferred := st.Locale
		if !row.ExpiresAt.After(s.now()) {
			preferred = ""
		}
		st = &state{Locale: preferred, UserID: uid, Generation: a.Generation, LegacyInputThrough: st.LegacyInputThrough}
	}
	st.IdentityFailure = failure
	if st.PublicGeneration == "" {
		st.PublicGeneration, err = nonce()
		if err != nil {
			return nil, false, err
		}
	}
	return &view{service: s, session: row, state: st, actor: a, update: u}, isolatedLegacy && retired, nil
}

// prepareContact resolves durable locale and records reachability without granting ownership.
func (v *view) prepareContact(ctx context.Context) (*rd.Contact, error) {
	s, st, u, a := v.service, v.state, v.update, &v.actor
	botID, uid := a.BotID, a.UserID
	contact, err := s.d.Store.Contact(ctx, botID, u.UserID)
	if err != nil && !errors.Is(err, rt.ErrNotFound) {
		return nil, err
	}
	if contact == nil {
		contact = &rd.Contact{BotID: botID, TelegramUserID: u.UserID}
	}
	// Keep an active session's language; outside it the contact is the durable preference.
	st.Locale = supportedLocale(st.Locale)
	if st.Locale == "" {
		st.Locale = supportedLocale(contact.Locale)
	}
	if st.Locale == "" {
		st.Locale = supportedLocale(u.Locale)
	}
	a.Locale = st.Locale
	if st.Locale == "" && s.d.Content != nil {
		content, e := s.d.Content.Content(ctx, *a)
		if e != nil {
			return nil, e
		}
		st.Locale = supportedLocale(content.DefaultLocale)
	}
	st.Locale = locale(st.Locale)
	a.Locale = st.Locale
	contact.ChatID = u.ChatID
	contact.UserID = uid
	contact.Reachable = true
	contact.Locale = a.Locale
	if err = s.d.Store.SaveContact(ctx, contact); err != nil {
		return nil, err
	}
	return contact, nil
}

// replay returns only a current authorized cached reply and ignores older updates.
func (v *view) replay(ctx context.Context) ([]rt.Message, error) {
	s, st, u := v.service, v.state, v.update
	botID := v.actor.BotID
	if u.ID != 0 && st.LastUpdate == u.ID {
		for _, message := range st.Reply {
			if message.PublicReply != nil {
				if err := s.AuthorizePublicReply(ctx, botID, message); err != nil {
					return nil, err
				}
			}
		}
		if st.ReplyOwner != 0 {
			if e := s.AuthorizeDelivery(ctx, botID, u.UserID, st.ReplyOwner); e != nil {
				return nil, e
			}
		}
		return st.Reply, nil
	}
	return nil, nil
}

// resolveAction validates callback context and returns whether a foreign panel must be ignored.
func (v *view) resolveAction(ctx context.Context) (action, bool, bool, error) {
	st, row, u, a := v.state, v.session, v.update, v.actor
	var err error
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
			return action{}, false, false, err
		}
	}
	if stale {
		if st.PanelTarget == 0 && row.PanelMessageID > 0 && row.PanelMessageID == u.MessageID {
			st.PanelTarget = u.MessageID
		}
		rt.AnswerCallback(ctx, text(a.Locale, "restored"))
		// A callback from an older physical panel cannot take over the active target.
		if st.PanelTarget != 0 && u.MessageID != 0 && st.PanelTarget != u.MessageID {
			return action{}, false, true, nil
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
	} else if stale && a.UserID == 0 {
		// An expired/unbound callback cannot claim an unconfirmed physical panel.
		st.PanelTarget = 0
	}
	if u.CallbackData == "" && strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		st.PanelEpoch, err = nonce()
		if err != nil {
			return action{}, false, false, err
		}
		v.replace = true
		st.PanelTarget = 0
	}
	// Ref is the operation context, not a render or CAS version. Keep it for deltas.
	if st.Ref == "" || (!stale && u.CallbackData != "" && act.Kind != "delta" && act.Kind != "quantity" && act.Kind != "more_quantity" && act.Kind != "quantity_input" && act.Kind != "details" && act.Kind != "purchase" && act.Kind != "noop" && act.Kind != "label" && act.Kind != "check_payment") || v.replace {
		st.Ref, err = nonce()
		if err != nil {
			return action{}, false, false, err
		}
	}
	v.explicit = u.CallbackData != "" && !stale && (act.Kind == "account" || act.Kind == "continue_checkout")
	return act, stale, false, nil
}

// executeUpdate executes a validated action and persists replay/legacy-input fences.
func (v *view) executeUpdate(ctx context.Context, act action, stale, isolatedLegacy, retiredLegacy bool, contact *rd.Contact) ([]rt.Message, error) {
	s, st, u, a := v.service, v.state, v.update, v.actor
	uid := a.UserID
	var err error
	oldActions := st.Actions
	// Only an active legacy binding flow may consume and delete sensitive input.
	// Ordinary text (including numbers) is future support content, not cleanup.
	bindingInput := st.Step == "email" || st.Step == "code"
	if (isolatedLegacy || legacyStep(st.Step)) && u.CallbackData == "" && !strings.HasPrefix(strings.TrimSpace(u.Text), "/") && u.ID > st.LegacyInputThrough {
		st.LegacyInputThrough = u.ID
	}
	st.Actions = map[string]action{}
	st.ReplyOwner = 0
	if st.IdentityFailure != "" {
		v.say(text(a.Locale, st.IdentityFailure))
	} else if retiredLegacy {
		err = v.perform(ctx, action{Kind: "bind"})
	} else if stale {
		err = v.renderCurrent(ctx)
	} else if u.CallbackData != "" {
		err = v.navigate(ctx, act)
	} else {
		err = v.input(ctx, strings.TrimSpace(u.Text))
	}
	if err != nil {
		key := identityFailureKey(err)
		if errors.Is(err, c.ErrWalletOnlyPaymentRequired) {
			key = "wallet_only_payment"
		} else if errors.Is(err, c.ErrInsufficientBalance) {
			key = "insufficient_balance"
		}
		if key == "" && !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrUnavailable) && !errors.Is(err, c.ErrPriceChanged) {
			return nil, err
		}
		if key == "" {
			key = "failed"
		}
		v.say(text(a.Locale, key))
		v.homeButton()
	}
	rt.AnswerCallback(ctx, "")
	// Preserve commands and ordinary consultation text. Legacy binding inputs
	// remain isolated until the website binding transition is available.
	if bindingInput && uid != 0 && u.CallbackData == "" && u.MessageID > 0 && !strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		v.messages = append([]rt.Message{{Purpose: "cleanup", Operation: "delete", TargetMessageID: u.MessageID, ChatID: u.ChatID}}, v.messages...)
	}
	st.LastUpdate = u.ID
	if len(v.messages) == 0 {
		st.Actions = oldActions
	}
	messages, err := v.save(ctx)
	if err != nil {
		return nil, err
	}
	// Persist explicit selection in this update, after the session has committed.
	// A retry can repair a failed contact save from the committed session locale.
	if contact.Locale != st.Locale {
		contact.Locale = st.Locale
		if err = s.d.Store.SaveContact(ctx, contact); err != nil {
			return nil, err
		}
	}
	return messages, nil
}
func (v *view) save(ctx context.Context) ([]rt.Message, error) {
	v.retainMembershipPending()
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
			if v.update.CallbackData != "" {
				m.TargetKind = v.update.MessageKind
			}
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
	v.session.ExpiresAt = v.service.now().Add(30 * time.Minute)
	if v.actor.UserID == 0 {
		for index := range v.messages {
			message := &v.messages[index]
			message.TargetType = rt.TargetShopPrivate
			message.PublicReply = &rt.PublicReplyRef{BotID: v.actor.BotID, TelegramID: v.actor.TelegramID, Generation: v.state.PublicGeneration, ExpiresAt: v.session.ExpiresAt}
		}
	}
	v.captureMembershipCompletion()
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
	if owner == 0 || v.actor.UserID == 0 {
		return c.ErrDenied
	}
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
	case "/start":
		if len(parts) > 1 {
			return v.startProduct(ctx, parts[1:])
		}
		return v.home(ctx)
	case "/cancel":
		return v.home(ctx)
	case "/back":
		return v.goBack(ctx)
	case "/memberships":
		return v.navigate(ctx, action{Kind: "memberships"})
	case "/help":
		return v.navigate(ctx, action{Kind: "help"})
	case "/language":
		return v.navigate(ctx, action{Kind: "language"})
	}
	switch v.state.Step {
	case "quantity":
		// Retire persisted text-input sessions without interpreting consultation
		// text as a purchase quantity. Keep the selection and restore buttons.
		v.state.Step = ""
		v.state.MoreQuantity = true
		v.say(text(v.actor.Locale, "use_buttons"))
		return v.purchase(ctx, false)
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
		if unavailable, _ := ctx.Value(supportUnavailableKey{}).(bool); unavailable {
			// Consultation feedback must be a new message, not a silent edit of an old shop panel.
			v.messages = append(v.messages, rt.Message{ChatID: v.update.ChatID, Purpose: "public_notice", Operation: "send", Text: text(v.actor.Locale, "support_unavailable")})
			return nil
		}
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
	return v.renderPage(ctx, v.currentPage())
}
func (v *view) perform(ctx context.Context, a action) error {
	if v.actor.UserID == 0 {
		handled, err := v.guestAction(ctx, a)
		if handled {
			return err
		}
	}
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
	case "continue_checkout":
		if !v.explicit {
			return c.ErrDenied
		}
		return v.purchase(ctx, true)
	case "confirm":
		return v.confirm(ctx)
	case "memberships":
		return v.memberships(ctx, a)
	case "membership_renew":
		return v.renewMembership(ctx, a)
	case "membership":
		return v.existingMembership(ctx, a)
	case "membership_apply":
		return v.applyScopedMembership(ctx, a)
	case "membership_request":
		return v.requestMembership(ctx, a)
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
	default:
		return v.performSettings(ctx, a)
	}
}

// performSettings dispatches account, locale and legacy-binding views after guest gating.
func (v *view) performSettings(ctx context.Context, a action) error {
	switch a.Kind {
	case "language":
		if v.state.Current.Kind != "language" {
			page := v.currentPage()
			v.state.ReturnView = &page
		}
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
		if v.state.ReturnView != nil {
			page := *v.state.ReturnView
			v.state.ReturnView = nil
			return v.renderPage(ctx, page)
		}
		v.state.Current = v.state.ReturnPage
		return v.renderCurrent(ctx)
	case "account":
		return v.accountPanel(ctx, a)
	case "bind":
		v.state.Current = a
		v.state.Step, v.state.Email = "", ""
		if v.content.BindingURL == "" {
			v.say(text(v.actor.Locale, "binding_unavailable"))
		} else {
			v.say(text(v.actor.Locale, "binding_website"))
			v.urlButton(text(v.actor.Locale, "open_binding"), v.content.BindingURL)
		}
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

// accountPanel reads and renders only the already-authorized current account.
func (v *view) accountPanel(ctx context.Context, a action) error {
	acc, err := v.service.d.Accounts.Account(ctx, v.actor)
	if err != nil {
		return err
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
	v.membershipButton()
	start := len(v.panel().Buttons)
	v.button(text(v.actor.Locale, "bind"), action{Kind: "bind"})
	v.button(text(v.actor.Locale, "history"), action{Kind: "history"})
	v.columns(2, start)
	v.homeButton()
	return nil
}

func chooseOwner(owner, current uint) uint {
	if owner == 0 {
		return current
	}
	return owner
}
