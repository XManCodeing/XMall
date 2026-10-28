package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
}
type view struct {
	service  *Service
	session  *rd.Session
	state    *state
	actor    c.Actor
	update   rt.Update
	content  c.Content
	messages []rt.Message
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
	st := &state{Locale: a.Locale, UserID: uid}
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
	if st.LastUpdate == u.ID && u.ID != 0 {
		if st.ReplyOwner != 0 {
			if e := s.AuthorizeDelivery(ctx, botID, u.UserID, st.ReplyOwner); e != nil {
				return nil, e
			}
		}
		return st.Reply, nil
	}
	var act action
	if u.CallbackData != "" {
		parts := strings.Split(u.CallbackData, ":")
		if len(parts) != 4 || parts[0] != "s" || parts[1] != st.Ref || parts[2] != strconv.FormatUint(row.Version, 36) {
			return []rt.Message{{IdentityStamp: a.Generation, OwnerUserID: uid, ChatID: u.ChatID, Text: text(a.Locale, "stale")}}, nil
		}
		var ok bool
		act, ok = st.Actions[parts[3]]
		if !ok {
			return []rt.Message{{IdentityStamp: a.Generation, OwnerUserID: uid, ChatID: u.ChatID, Text: text(a.Locale, "stale")}}, nil
		}
	}
	ref, err := nonce()
	if err != nil {
		return nil, err
	}
	st.Ref = ref
	st.ReplyOwner = 0
	st.Actions = map[string]action{}
	v := &view{service: s, session: row, state: st, actor: a, update: u}
	if s.d.Content != nil {
		v.content, err = s.d.Content.Content(ctx, a)
		if err != nil {
			return nil, err
		}
	}
	if u.CallbackData != "" {
		err = v.perform(ctx, act)
	} else {
		err = v.input(ctx, strings.TrimSpace(u.Text))
	}
	if err != nil {
		// Never expose backend errors (which can contain gateway credentials or personal data).
		if !errors.Is(err, c.ErrDenied) && !errors.Is(err, c.ErrUnavailable) && !errors.Is(err, c.ErrPriceChanged) {
			return nil, err
		}
		key := "failed"
		if errors.Is(err, c.ErrPriceChanged) {
			key = "price_changed"
			st.Quote = nil
			st.Operation = ""
			st.Step = ""
		}
		v.say(text(a.Locale, key))
		v.homeButton()
	}
	st.LastUpdate = u.ID
	for index := range v.messages {
		v.messages[index].IdentityStamp = v.actor.Generation
		v.messages[index].OwnerUserID = v.actor.UserID
		if st.ReplyOwner != 0 {
			v.messages[index].OwnerUserID = st.ReplyOwner
		}
	}
	st.Reply = v.messages
	raw, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	cipher, err := crypto.Encrypt(s.key, string(raw))
	if err != nil {
		return nil, err
	}
	row.DataCipher = cipher
	row.State = st.Step
	row.ExpiresAt = s.now().Add(30 * time.Minute)
	if err = s.d.Store.SaveSession(ctx, row, row.Version); err != nil {
		return nil, err
	}
	return v.messages, nil
}

// DeliveryStamp also supports business-event messages produced outside Handle.
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
	contact, err := s.d.Store.Contact(ctx, botID, telegramID)
	if err != nil {
		return err
	}
	if contact == nil || !contact.Reachable || contact.ChatID != telegramID {
		return c.ErrDenied
	}
	a := c.Actor{BotID: botID, TelegramID: telegramID}
	current, err := s.d.Identity.Resolve(ctx, a, false)
	if err != nil {
		return err
	}
	if current == 0 {
		return c.ErrDenied
	}
	a.UserID = current
	if current == userID {
		return nil
	}
	ids, err := s.d.Identity.HistoryUsers(ctx, a)
	if err != nil {
		return err
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
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (v *view) say(t string) {
	v.messages = append(v.messages, rt.Message{ChatID: v.update.ChatID, Text: t})
}
func (v *view) button(label string, a action) {
	if len(v.messages) == 0 {
		v.say("")
	}
	key := strconv.FormatInt(int64(len(v.state.Actions)), 36)
	v.state.Actions[key] = a
	data := fmt.Sprintf("s:%s:%s:%s", v.state.Ref, strconv.FormatUint(v.session.Version+1, 36), key)
	i := len(v.messages) - 1
	v.messages[i].Buttons = append(v.messages[i].Buttons, []rt.Button{{Text: label, Data: data}})
}
func (v *view) homeButton() { v.button(text(v.actor.Locale, "back"), action{Kind: "home"}) }
func (v *view) home() {
	v.state.Step = ""
	v.state.Quote = nil
	v.state.Operation = ""
	v.state.Email = ""
	welcome := v.content.Welcome
	if welcome == "" {
		welcome = text(v.actor.Locale, "welcome")
	}
	v.say(welcome)
	if v.content.Menu == nil {
		for _, k := range []string{"catalog", "orders", "account", "bind", "history", "language", "help"} {
			v.button(text(v.actor.Locale, k), action{Kind: k})
		}
		return
	}
	for _, item := range v.content.Menu {
		if item.URL != "" {
			u, e := url.Parse(item.URL)
			if e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && item.Label != "" {
				v.messages[len(v.messages)-1].Buttons = append(v.messages[len(v.messages)-1].Buttons, []rt.Button{{Text: item.Label, URL: u.String()}})
			}
			continue
		}
		switch item.Action {
		case "catalog", "orders", "account", "language", "help":
			label := item.Label
			if label == "" {
				label = text(v.actor.Locale, item.Action)
			}
			v.button(label, action{Kind: item.Action})
		}
	}
	for _, k := range []string{"bind", "history"} {
		v.button(text(v.actor.Locale, k), action{Kind: k})
	}
}
func (v *view) input(ctx context.Context, t string) error {
	command := strings.Fields(t)
	cmd := ""
	if len(command) > 0 {
		cmd = strings.Split(command[0], "@")[0]
	}
	switch cmd {
	case "/start", "/cancel", "/back":
		v.home()
		return nil
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
			return nil
		}
		v.state.Selection.Quantity = n
		return v.quote(ctx)
	case "email":
		if len(t) > 254 || !strings.Contains(t, "@") {
			v.say(text(v.actor.Locale, "email"))
			return nil
		}
		if e := v.service.d.Identity.SendBindCode(ctx, v.actor, t); e != nil {
			return e
		}
		v.state.Email = t
		v.state.Step = "code"
		v.say(text(v.actor.Locale, "code"))
		v.homeButton()
		return nil
	case "code":
		if len(t) < 4 || len(t) > 12 {
			return c.ErrDenied
		}
		id, e := v.service.d.Identity.Bind(ctx, v.actor, v.state.Email, t)
		if e != nil {
			return e
		}
		if id == 0 {
			return c.ErrDenied
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
		v.home()
		return nil
	default:
		v.home()
		return nil
	}
}
func (v *view) perform(ctx context.Context, a action) error {
	switch a.Kind {
	case "home":
		v.home()
	case "help":
		help := v.content.Help
		if help == "" {
			help = text(v.actor.Locale, "help_text")
		}
		v.say(help)
		v.homeButton()
	case "language":
		v.say(text(v.actor.Locale, "language"))
		for _, l := range []string{"zh-CN", "zh-TW", "en-US"} {
			v.button(l, action{Kind: "locale", Value: l})
		}
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
		v.home()
	case "catalog":
		cats, e := v.service.d.Catalog.Categories(ctx, v.actor)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "catalog"))
		page := a.Page
		if page < 1 {
			page = 1
		}
		start := min((page-1)*8, len(cats))
		end := min(start+8, len(cats))
		for _, cat := range cats[start:end] {
			v.button(cat.Name, action{Kind: "products", ID: cat.ID, Page: 1})
		}
		v.pages(action{Kind: "catalog", Page: page}, end < len(cats))
		v.homeButton()
	case "products":
		items, more, e := v.service.d.Catalog.Products(ctx, v.actor, a.ID, a.Page)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "catalog"))
		for _, p := range items {
			label := p.Title
			if p.Price != "" {
				label += " · " + p.Currency + " " + p.Price
			}
			if p.Stock != "" {
				label += " · " + p.Stock
			}
			v.button(label, action{Kind: "product", ID: p.ID})
		}
		v.pages(a, more)
		v.homeButton()
	case "product":
		p, e := v.service.d.Catalog.Product(ctx, v.actor, a.ID)
		if e != nil {
			return e
		}
		if p == nil {
			return c.ErrDenied
		}
		v.state.Selection = c.Selection{ProductID: p.ID}
		v.state.Quote = nil
		v.say(p.Title + "\n" + p.Description)
		if p.Price != "" {
			priceLabel, notice := "公开价", "实际应付金额（含会员优惠）以选择数量后的订单预览为准。"
			if v.actor.Locale == "en-US" {
				priceLabel, notice = "Public price", "Payable amount (including member discounts) is shown in the order preview after choosing quantity."
			} else if v.actor.Locale == "zh-TW" {
				priceLabel, notice = "公開價", "實際應付金額（含會員優惠）以選擇數量後的訂單預覽為準。"
			}
			v.say(priceLabel + ": " + p.Currency + " " + p.Price + "\n" + notice)
		}
		if p.Stock != "" {
			v.say(p.Stock)
		}
		if p.Unsupported {
			v.say(text(v.actor.Locale, "unsupported"))
		} else if len(p.SKUs) > 0 {
			for _, sku := range p.SKUs {
				label := sku.Name
				if sku.Price != "" {
					label += " · " + p.Currency + " " + sku.Price
				}
				if sku.Stock != "" {
					label += " · " + sku.Stock
				}
				v.button(label, action{Kind: "sku", ID: sku.ID})
			}
		} else {
			v.state.Step = "quantity"
			v.say(text(v.actor.Locale, "quantity"))
		}
		v.homeButton()
	case "sku":
		p, e := v.service.d.Catalog.Product(ctx, v.actor, v.state.Selection.ProductID)
		if e != nil {
			return e
		}
		if p == nil || p.Unsupported {
			return c.ErrDenied
		}
		valid := false
		for _, sku := range p.SKUs {
			if sku.ID == a.ID {
				valid = true
				v.say(sku.Name)
				if sku.Price != "" {
					v.say(p.Currency + " " + sku.Price)
				}
				if sku.Stock != "" {
					v.say(sku.Stock)
				}
			}
		}
		if !valid {
			return c.ErrDenied
		}
		v.state.Selection.SKUID = a.ID
		v.state.Step = "quantity"
		v.say(text(v.actor.Locale, "quantity"))
		v.homeButton()
	case "confirm":
		if v.state.Quote == nil || v.state.Operation == "" {
			return c.ErrDenied
		}
		// Creation replays an existing operation BEFORE validating today's price.
		o, e := v.service.d.Orders.Create(ctx, v.actor, v.state.Selection, *v.state.Quote, v.state.Operation)
		if errors.Is(e, c.ErrPriceChanged) {
			v.say(text(v.actor.Locale, "price_changed"))
			v.state.Quote = nil
			v.state.Operation = ""
			return v.quote(ctx)
		}
		if e != nil {
			return e
		}
		if o == nil || o.UserID != v.actor.UserID {
			return c.ErrDenied
		}
		v.state.Quote = nil
		v.state.Step = ""
		return v.order(ctx, o.ID, v.actor.UserID)
	case "orders":
		return v.orders(ctx, v.actor.UserID, a.Page)
	case "history":
		ids, e := v.service.d.Identity.HistoryUsers(ctx, v.actor)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "history"))
		for _, id := range ids {
			v.button(fmt.Sprintf("#%d", id), action{Kind: "history_orders", Owner: id, Page: 1})
		}
		v.homeButton()
	case "history_orders":
		if err := v.orders(ctx, a.Owner, a.Page); err != nil {
			return err
		}
		if _, ok := v.service.d.Accounts.(c.HistoryAccounts); ok {
			v.button(text(v.actor.Locale, "history_account"), action{Kind: "history_account", Owner: a.Owner})
		}
		return nil
	case "history_account", "history_refunds":
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
			account, err := reader.HistoryAccount(ctx, v.actor, a.Owner)
			if err != nil {
				return err
			}
			if account == nil || account.ID != a.Owner {
				return c.ErrDenied
			}
			v.say(fmt.Sprintf("%s\n#%d\n%s %s", text(v.actor.Locale, "history_account"), account.ID, account.Balance, account.Currency))
			v.button(text(v.actor.Locale, "history_refunds"), action{Kind: "history_refunds", Owner: a.Owner, Page: 1})
		} else {
			if a.Page < 1 {
				a.Page = 1
			}
			rows, more, err := reader.HistoryRefunds(ctx, v.actor, a.Owner, a.Page)
			if err != nil {
				return err
			}
			v.say(text(v.actor.Locale, "history_refunds"))
			for _, row := range rows {
				v.say(fmt.Sprintf("%s\n%s %s\n%s", row.CreatedAt.Format(time.RFC3339), row.Amount, row.Currency, row.Reference))
			}
			v.pages(a, more)
		}
		v.homeButton()
	case "order":
		return v.order(ctx, a.ID, a.Owner)
	case "channels":
		chs, e := v.service.d.Payments.Channels(ctx, v.actor, a.ID)
		if e != nil {
			return e
		}
		v.say(text(v.actor.Locale, "channels"))
		paymentOperation, e := nonce()
		if e != nil {
			return e
		}
		for _, ch := range chs {
			v.button(ch.Name, action{Kind: "pay", ID: a.ID, Owner: ch.ID, Value: fmt.Sprintf("tgp:%d:%d:%s:%d", v.actor.BotID, v.actor.UserID, paymentOperation, ch.ID)})
		}
		v.homeButton()
	case "pay":
		p, e := v.service.d.Payments.Pay(ctx, v.actor, a.ID, a.Owner, a.Value)
		if errors.Is(e, c.ErrPaymentUncertain) {
			v.say(text(v.actor.Locale, "payment_uncertain"))
			v.button(text(v.actor.Locale, "refresh"), action{Kind: "order", ID: a.ID, Owner: v.actor.UserID})
			v.homeButton()
			return nil
		}
		if e != nil {
			return e
		}
		return v.payment(p, a.ID)
	case "account":
		acc, e := v.service.d.Accounts.Account(ctx, v.actor)
		if e != nil {
			return e
		}
		if acc == nil || acc.ID != v.actor.UserID {
			return c.ErrDenied
		}
		v.say(fmt.Sprintf("%s\n#%d\n%s\n%s %s", text(v.actor.Locale, "account"), acc.ID, acc.Email, acc.Balance, acc.Currency))
		v.homeButton()
	case "bind":
		v.state.Step = "email"
		v.say(text(v.actor.Locale, "email"))
		v.homeButton()
	default:
		return c.ErrDenied
	}
	return nil
}
func (v *view) quote(ctx context.Context) error {
	p, e := v.service.d.Catalog.Product(ctx, v.actor, v.state.Selection.ProductID)
	if e != nil {
		return e
	}
	n := v.state.Selection.Quantity
	if p == nil || p.Unsupported || n < p.Min || (p.Max > 0 && n > p.Max) {
		return c.ErrDenied
	}
	q, e := v.service.d.Orders.Preview(ctx, v.actor, v.state.Selection)
	if e != nil {
		return e
	}
	if q == nil {
		return c.ErrUnavailable
	}
	op, e := nonce()
	if e != nil {
		return e
	}
	v.state.Operation = fmt.Sprintf("tg:%d:%d:%s", v.actor.BotID, v.actor.UserID, op)
	v.state.Quote = q
	v.state.Step = "confirm"
	v.say(fmt.Sprintf("%s\n%s × %d\n%s %s", text(v.actor.Locale, "quote"), p.Title, n, q.Amount, q.Currency))
	v.button(text(v.actor.Locale, "confirm"), action{Kind: "confirm"})
	v.homeButton()
	return nil
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
func (v *view) orders(ctx context.Context, owner uint, page int) error {
	if e := v.allowed(ctx, owner); e != nil {
		return e
	}
	if page < 1 {
		page = 1
	}
	items, more, e := v.service.d.Orders.List(ctx, v.actor, owner, page)
	if e != nil {
		return e
	}
	v.say(text(v.actor.Locale, "orders"))
	for _, o := range items {
		if o.UserID != owner {
			return c.ErrDenied
		}
		v.button(o.Number+" · "+o.Status, action{Kind: "order", ID: o.ID, Owner: owner})
	}
	kind := "orders"
	if owner != v.actor.UserID {
		kind = "history_orders"
	}
	v.pages(action{Kind: kind, Owner: owner, Page: page}, more)
	v.homeButton()
	return nil
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
	v.state.ReplyOwner = owner
	v.say(fmt.Sprintf("%s\n%s\n%s %s", o.Number, o.Status, o.Amount, o.Currency))
	if o.ExpiresAt != nil {
		v.say(text(v.actor.Locale, "expires") + o.ExpiresAt.Format(time.RFC3339))
	}
	for _, delivery := range o.Delivered {
		for len([]rune(delivery)) > 3500 {
			r := []rune(delivery)
			v.say(string(r[:3500]))
			delivery = string(r[3500:])
		}
		if delivery != "" {
			v.say(delivery)
		}
	}
	if owner == v.actor.UserID && o.Pending {
		v.button(text(v.actor.Locale, "channels"), action{Kind: "channels", ID: id})
	}
	v.button(text(v.actor.Locale, "refresh"), action{Kind: "order", ID: id, Owner: owner})
	v.homeButton()
	return nil
}
func (v *view) pages(a action, more bool) {
	if a.Page > 1 {
		prev := a
		prev.Page--
		v.button("‹", prev)
	}
	if more {
		a.Page++
		v.button("›", a)
	}
}
