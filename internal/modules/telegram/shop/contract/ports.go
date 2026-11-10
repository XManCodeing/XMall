package contract

import (
	"context"
	"errors"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	walletcontract "github.com/XManCodeing/XMall/internal/modules/wallet/contract"
	"time"
)

var ErrDenied = errors.New("shop operation denied")
var ErrPriceChanged = errors.New("shop price changed")
var ErrPaymentUncertain = errors.New("payment creation outcome unknown")
var ErrUnavailable = errors.New("shop operation unavailable")

// ErrInsufficientBalance reuses the wallet contract instead of inventing another balance error.
var ErrInsufficientBalance = walletcontract.ErrInsufficientBalance
var ErrWalletOnlyPaymentRequired = walletcontract.ErrOnlyPaymentRequired

// PaymentRetryError offers an explicit retry using a backend-authorized successor.
// Unknown or processing attempts must never produce this error.
type PaymentRetryError struct {
	OperationKey string
}

// Error returns a safe diagnostic without exposing the operation key.
func (e *PaymentRetryError) Error() string { return "payment attempt unavailable; retry permitted" }

// ErrRegistrationDisabled and ErrAccountDisabled are terminal user-facing policy failures.
var ErrRegistrationDisabled = errors.New("telegram registration disabled")
var ErrAccountDisabled = errors.New("telegram account disabled")
var ErrInviteRequired = errors.New("telegram registration requires invitation")

// ProductLinkAccess validates current membership-product/target/bot association.
// Implementations must read current relations; missing, inactive or unrelated products fail closed.
type ProductLinkAccess interface {
	ResolveProductLink(context.Context, string) (uint, error)
	AuthorizeProductLink(context.Context, Actor, uint) error
}

type Actor struct {
	// UsernamePresent is true only when Username came from an actual Telegram user update.
	UsernamePresent  bool
	BotID            uint
	TelegramID       int64
	UserID           uint
	Generation       string
	Username, Locale string
}
type Account struct {
	ID                       uint
	Email, Balance, Currency string
}
type Category struct {
	ID   uint
	Name string
}
type SKU struct {
	// MembershipTerm is a read-only projection of this SKU's shared membership configuration.
	MembershipTerm *MembershipTerm
	ID             uint
	Name           string
	Price          string // Public/base unit price, never a member-price quote.
	Stock          string // Policy-filtered display; never an unmasked quantity.
	Available      bool
	MaxQuantity    *int // Public exact stock bound only; nil for hidden/unknown/unlimited stock.
}

// MembershipTerm identifies the existing SKU term; months always mean fixed 30-day periods.
type MembershipTerm struct {
	Kind   string
	Months int64
}

type Product struct {
	// Membership is based on configuration existence, never purchase_type or enabled.
	Membership bool
	// GroupNames contains only administrator-registered public names in configuration order.
	GroupNames                           []string
	ID                                   uint
	Title, Description                   string
	Summary, Image, URL, FulfillmentType string
	Available                            bool
	MaxQuantity                          *int   // Public exact stock bound only; nil for hidden/unknown/unlimited stock.
	Price, Currency                      string // Public/base price; payable total comes from PreviewOrder.
	Stock                                string
	SKUs                                 []SKU
	Min, Max                             int
	Unsupported                          bool
}
type Selection struct {
	ProductID, SKUID uint
	Quantity         int
}
type Quote struct {
	Amount, Currency, Fingerprint string
	Discounted                    bool `json:"-"` // Presentation-only; excluded from the existing idempotency request hash.
}
type Order struct {
	ID, UserID                       uint
	Number, Status, Amount, Currency string
	ExpiresAt                        *time.Time
	Pending                          bool
	Delivered                        []string
	Items                            []OrderItem
	CreatedAt                        time.Time
}
type OrderItem struct {
	ProductID, SKUID uint
	Title, SKUName   string
	Quantity         int
}
type Channel struct {
	ID     uint
	Name   string
	Wallet bool
}
type Payment struct {
	Amount, Currency, PayURL, QRCode string
	OrderNumber                      string
	// QRCodeKind is "image" only when the gateway or channel explicitly declares it.
	// Empty / "text" means scan payload and must never trigger an HTTP probe.
	QRCodeKind      string
	InteractionMode string
	// QRImage is validated PNG media fetched by the infrastructure adapter.
	QRImage   []byte
	ExpiresAt *time.Time
	Paid      bool
}

// MembershipGroup mirrors the website's public, secret-free membership projection.
// IDs must be canonical positive decimal strings; permanent groups have no deadline.
type MembershipGroup struct {
	TargetID              string     `json:"target_id"`
	Title                 string     `json:"title"`
	BeneficiaryTelegramID string     `json:"beneficiary_telegram_id"`
	Permanent             bool       `json:"permanent"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	State                 string     `json:"state"`
	MemberStatus          string     `json:"member_status,omitempty"`
	CanRequest            bool       `json:"can_request"`
	// Replaced compares this order's immutable route with the current route, not other orders.
	Replaced bool `json:"replaced"`
	// HistoricalChats contains distinct replaced snapshot chat IDs and route versions, never invented titles.
	HistoricalChats []string `json:"historical_chats,omitempty"`
}

// MembershipPage uses ascending target IDs and an exclusive cursor, bounded by limit.
type MembershipPage struct {
	Items      []MembershipGroup `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// MembershipInvitation discloses an authorized existing or explicitly requested link.
// State describes invitation execution/lifetime, not membership entitlement. In particular,
// expired/revoked never imply that a later purchase's membership has expired or been revoked.
type MembershipInvitation struct {
	// Permanent explicitly identifies a non-expiring invite; nil expiry alone is invalid.
	Permanent      bool                  `json:"permanent"`
	InviteDelivery *rt.InviteDeliveryRef `json:"-"`
	State          string                `json:"state"`
	InviteURL      string                `json:"invite_url,omitempty"`
	ExpiresAt      *time.Time            `json:"expires_at,omitempty"`
}

// MembershipAccess revalidates Actor's current verified account, Telegram ID and
// generation on every call. Historical account grants never authorize access.
// List is read-only and never creates accounts, invitations or Telegram requests.
// Request durably deduplicates by beneficiary/target, rechecks eligibility and
// returns an existing live invite or schedules work; unknown outcomes never retry.
// Both methods must restrict execution routes to Actor.BotID.
type MembershipAccess interface {
	List(context.Context, Actor, uint, int) (MembershipPage, error)
	Request(context.Context, Actor, uint) (MembershipInvitation, error)
}

// MembershipReader revalidates actor, beneficiary, route and invitation scope on each read.
// GetExisting never generates, renews, schedules or requests an invitation.
type MembershipReader interface {
	GetExisting(context.Context, Actor, uint) (MembershipInvitation, error)
}

// OrderMembershipReader returns only targets captured in this order's immutable
// fulfillment snapshot, never the account's complete membership collection.
// Targets validates order ownership and current beneficiary; historical grants do not authorize links.
type OrderMembershipReader interface {
	Targets(context.Context, Actor, uint, uint) ([]MembershipGroup, error)
}

type IdentityGeneration interface {
	// Generation changes when the external identity is removed/re-created.
	Generation(context.Context, Actor) (string, error)
}

type Identity interface {
	Resolve(context.Context, Actor, bool) (uint, error)
	SendBindCode(context.Context, Actor, string) error
	Bind(context.Context, Actor, string, string) (uint, error)
	HistoryUsers(context.Context, Actor) ([]uint, error)
}
type Catalog interface {
	Categories(context.Context, Actor) ([]Category, error)
	Products(context.Context, Actor, uint, int) ([]Product, bool, error)
	Product(context.Context, Actor, uint) (*Product, error)
}
type Orders interface {
	Preview(context.Context, Actor, Selection) (*Quote, error)
	// Create must atomically enforce expected quote and deduplicate operationKey.
	// No fallback to a non-idempotent creation API is permitted.
	Create(context.Context, Actor, Selection, Quote, string) (*Order, error)
	List(context.Context, Actor, uint, int) ([]Order, bool, error)
	// Detail is read-only: it must never allocate new card secrets.
	Detail(context.Context, Actor, uint, uint) (*Order, error)
}

// OrdersFilter optionally supplies status-filtered reads and effective pending counts.
type OrdersFilter interface {
	ListFiltered(context.Context, Actor, uint, int, string) ([]Order, bool, error)
	PendingSummary(context.Context, Actor) (PendingSummary, error)
}
type PendingSummary struct {
	Count  int64
	Latest *Order
}

// OrdersCancel never grants writes through historical account access.
type OrdersCancel interface {
	Cancel(context.Context, Actor, uint) error
}
type Payments interface {
	Channels(context.Context, Actor, uint) ([]Channel, error)
	Pay(context.Context, Actor, uint, uint, string) (*Payment, error)
}
type MenuItem struct{ Action, Label, URL string }
type HelpItem struct {
	Key, Summary, Title, Body string
	ShowSupport               bool
}
type Content struct {
	WelcomeEnabled, HelpEnabled bool
	Welcome, Help               string
	HelpTitle, HelpIntro        string
	DisplayName, Description    string
	SupportURL, CoverURL        string
	BindingURL                  string // Verified site security entry; never a client-supplied redirect.
	CenterHint, SupportHint     string
	DefaultLocale               string
	HelpItems                   []HelpItem
	Menu                        []MenuItem
}
type ContentProvider interface {
	Content(context.Context, Actor) (Content, error)
}

type Accounts interface {
	Account(context.Context, Actor) (*Account, error)
}

// HistoryAccounts exposes reads only. Implementations must revalidate the
// binding and historical grant; no wallet creation or mutation is allowed.
type HistoryAccounts interface {
	HistoryAccount(context.Context, Actor, uint) (*Account, error)
	HistoryRefunds(context.Context, Actor, uint, int) ([]Refund, bool, error)
}
type Refund struct {
	Amount, Currency, Reference string
	CreatedAt                   time.Time
}
