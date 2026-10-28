package contract

import (
	"context"
	"errors"
	"time"
)

var ErrDenied = errors.New("shop operation denied")
var ErrPriceChanged = errors.New("shop price changed")
var ErrPaymentUncertain = errors.New("payment creation outcome unknown")
var ErrUnavailable = errors.New("shop operation unavailable")

type Actor struct {
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
	ID    uint
	Name  string
	Price string // Public/base unit price, never a member-price quote.
	Stock string // Policy-filtered display; never an unmasked quantity.
}
type Product struct {
	ID                 uint
	Title, Description string
	Price, Currency    string // Public/base price; payable total comes from PreviewOrder.
	Stock              string
	SKUs               []SKU
	Min, Max           int
	Unsupported        bool
}
type Selection struct {
	ProductID, SKUID uint
	Quantity         int
}
type Quote struct{ Amount, Currency, Fingerprint string }
type Order struct {
	ID, UserID                       uint
	Number, Status, Amount, Currency string
	ExpiresAt                        *time.Time
	Pending                          bool
	Delivered                        []string
}
type Channel struct {
	ID   uint
	Name string
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
type Payments interface {
	Channels(context.Context, Actor, uint) ([]Channel, error)
	Pay(context.Context, Actor, uint, uint, string) (*Payment, error)
}
type MenuItem struct{ Action, Label, URL string }
type Content struct {
	Welcome, Help string
	Menu          []MenuItem
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
