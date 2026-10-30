package contract

import (
	"context"
	"errors"
	"fmt"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"time"
)

var (
	ErrConflict  = errors.New("telegram configuration conflict")
	ErrDuplicate = fmt.Errorf("telegram bot instance already exists: %w", ErrConflict)
	ErrNotFound  = errors.New("telegram instance not found")
	ErrStale     = errors.New("telegram runtime generation lost")
)

// PanelStore is optional: older notification stores and test doubles need not
// implement panel lifecycle. PreparePanel persists the exact operation before HTTP;
// FinishPanel atomically records its result and the reusable physical target.
type PanelStore interface {
	PreparePanel(context.Context, *d.Delivery, *Message) error
	FinishPanel(context.Context, *d.Delivery, string, DeliveryResult, string, time.Time) error
}

// Store never exposes a database handle to the runtime.
type Store interface {
	List(context.Context) ([]d.Instance, error)
	Get(context.Context, uint) (*d.Instance, error)
	Create(context.Context, *d.Instance) error
	Configure(context.Context, uint, uint64, map[string]interface{}) error
	// SaveProfile persists Telegram Bot profile fields with an independent CAS version.
	SaveProfile(context.Context, uint, uint64, map[string]interface{}) error
	BeginGeneration(context.Context, uint, uint64) (*d.Instance, error)
	Heartbeat(context.Context, uint, uint64, string, string, uint64) error
	Accept(context.Context, uint, uint64, []d.Inbox, int64) error
	ClaimInbox(context.Context, uint, string, time.Time) (*d.Inbox, error)
	FinishInbox(context.Context, uint, string, string, string, time.Time) error
	Contact(context.Context, uint, int64) (*d.Contact, error)
	SaveContact(context.Context, *d.Contact) error
	GetSession(context.Context, uint, int64, int64) (*d.Session, error)
	SaveSession(context.Context, *d.Session, uint64) error
	Enqueue(context.Context, *d.Delivery) error
	ClaimDelivery(context.Context, uint, string, time.Time) (*d.Delivery, error)
	FinishDelivery(context.Context, uint, string, string, int64, string, time.Time) error
	ListDeliveries(context.Context, uint, uint, int, int) ([]d.Delivery, int64, error)
}
