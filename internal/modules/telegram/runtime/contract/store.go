package contract

import (
	"context"
	"errors"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"time"
)

var (
	ErrConflict = errors.New("telegram configuration conflict")
	ErrNotFound = errors.New("telegram instance not found")
	ErrStale    = errors.New("telegram runtime generation lost")
)

// Store never exposes a database handle to the runtime.
type Store interface {
	List(context.Context) ([]d.Instance, error)
	Get(context.Context, uint) (*d.Instance, error)
	Create(context.Context, *d.Instance) error
	Configure(context.Context, uint, uint64, map[string]interface{}) error
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
