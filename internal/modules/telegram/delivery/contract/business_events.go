package contract

import (
	"errors"
	"fmt"
	"github.com/XManCodeing/XMall/internal/modules/telegram/delivery/domain"
	"time"
)

var (
	ErrOperationConflict = errors.New("order operation key reused with different request")
	ErrInvalidSource     = errors.New("invalid telegram private-chat source")
	ErrLeaseLost         = errors.New("business event lease lost")
)

const (
	EventOrderPaid         = "order_paid"
	EventOrderFulfilled    = "order_fulfilled"
	EventRechargeSucceeded = "wallet_recharge_succeeded"
)

type Source struct {
	BotID         uint
	TelegramID    int64
	PrivateChatID int64
	// ExpectedIdentityID is a request-only binding generation, never persisted.
	ExpectedIdentityID uint
}

var ErrIdentityChanged = errors.New("telegram transaction identity changed or unavailable")

// TransactionIdentityGuard must use the caller's transaction and retain identity
// and active-user locks until commit, mutually excluding bind/unbind.
type TransactionIdentityGuard interface {
	LockTelegramIdentity(userID uint, source Source) error
}

func GuardIdentity(store any, userID uint, source *Source) error {
	if source == nil || source.ExpectedIdentityID == 0 {
		return nil // Legacy callers do not supply a binding generation.
	}
	if source.Validate() != nil || userID == 0 {
		return ErrIdentityChanged
	}
	guard, ok := store.(TransactionIdentityGuard)
	if !ok {
		return ErrIdentityChanged
	}
	return guard.LockTelegramIdentity(userID, *source)
}

func (s Source) Validate() error {
	if s.BotID == 0 || s.TelegramID <= 0 || s.PrivateChatID != s.TelegramID {
		return ErrInvalidSource
	}
	return nil
}

type Event domain.BusinessEvent
type OrderSource domain.OrderSource
type OrderOperation domain.OrderOperation

func (Event) TableName() string          { return (domain.BusinessEvent{}).TableName() }
func (OrderSource) TableName() string    { return (domain.OrderSource{}).TableName() }
func (OrderOperation) TableName() string { return (domain.OrderOperation{}).TableName() }

// BusinessStore must be bound to the SAME transaction as the business write.
type BusinessStore interface {
	FindOperation(key string) (*OrderOperation, error)
	ReserveOperation(key, hash string, userID uint) (*OrderOperation, bool, error)
	CompleteOperation(key string, orderID uint) error
	SaveSource(orderID, userID uint, source Source) error
	FindSource(orderID uint) (*OrderSource, error)
	Append(event *Event) error
}

// Provider is optional so legacy stores remain compatible. Enabled stores must
// propagate this capability to all transaction handles.
type Provider interface{ BusinessEvents() BusinessStore }

func From(store any) BusinessStore {
	if p, ok := store.(Provider); ok {
		return p.BusinessEvents()
	}
	return nil
}

func HasOrderSource(store any, orderID uint) (bool, error) {
	events := From(store)
	if events == nil {
		return false, nil
	}
	source, err := events.FindSource(orderID)
	return source != nil, err
}

// CurrentSourceResolver is optional. It resolves only a current, verified private
// contact inside the caller's business transaction. botID=0 requires one unique
// eligible bot; it must never select the first of several destinations.
type CurrentSourceResolver interface {
	ResolveCurrentSource(userID, botID uint) (*Source, error)
}

// CurrentOrderSourceResolver keeps order ownership reads in the order adapter.
// A resolved website destination is event-local, not a historical source row.
type CurrentOrderSourceResolver interface {
	ResolveCurrentOrderSource(orderID uint) (*OrderSource, error)
}

// AppendOrder prefers the explicit checkout source. Website orders without one
// may use a transaction-bound current recipient resolver; legacy mocks skip it.
func AppendOrder(store any, kind string, orderID, parentID, paymentID, fulfillmentID uint) error {
	events := From(store)
	if events == nil {
		return nil
	}
	source, err := events.FindSource(orderID)
	if err == nil && source == nil {
		if resolver, ok := store.(CurrentOrderSourceResolver); ok {
			source, err = resolver.ResolveCurrentOrderSource(orderID)
		}
	}
	if err != nil || source == nil {
		return err
	}
	key := fmt.Sprintf("%s:order:%d:fulfillment:%d", kind, orderID, fulfillmentID)
	return events.Append(&Event{EventKey: key, Kind: kind, OrderID: orderID, ParentOrderID: parentID, PaymentID: paymentID, FulfillmentID: fulfillmentID, UserID: source.UserID, BotID: source.BotID, TelegramID: source.TelegramID, PrivateChatID: source.PrivateChatID})
}

// DispatcherStore uses fenced leases. owner MUST be a unique claim token, not a
// stable worker ID, so an expired attempt cannot acknowledge a later claim.
// A crashed dispatcher can be reclaimed;
// Telegram acceptance followed by a crash is still an unknown/duplicate outcome.
type DispatcherStore interface {
	Scan(now time.Time, limit int) ([]Event, error)
	ScanBot(botID uint, now time.Time, limit int) ([]Event, error)
	Claim(id uint, owner string, now time.Time, ttl time.Duration) (bool, error)
	MarkDelivered(id uint, owner string, now time.Time) error
	Retry(id uint, owner string, now, availableAt time.Time) error
}
