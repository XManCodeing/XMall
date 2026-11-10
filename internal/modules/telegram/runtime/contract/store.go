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

// PanelSessionReader scans live private panels using a bounded exclusive ID
// cursor. Pending intent stays inside the existing encrypted session state.
type PanelSessionReader interface {
	PanelSessionsAfter(context.Context, uint, uint, int, time.Time) ([]d.Session, error)
}

// PanelStore is optional: older notification stores and test doubles need not
// implement panel lifecycle. PreparePanel persists the exact operation before HTTP;
// FinishPanel atomically records its result and the reusable physical target.
type PanelStore interface {
	PreparePanel(context.Context, *d.Delivery, *Message) error
	FinishPanel(context.Context, *d.Delivery, string, DeliveryResult, string, time.Time) error
}

// DeliveryExcludingStore is optional so existing stores and test doubles remain valid.
// The store creates a unique claim with a one-minute lease and fences generation.
// Exclusions are physical chat IDs (including negative group IDs), not identities.
type DeliveryExcludingStore interface {
	ClaimDeliveryExcluding(ctx context.Context, botID uint, generation uint64, now time.Time, excludedChatIDs []int64) (*d.Delivery, error)
}

// PayloadRetentionStore is for trusted dependency owners, never update payload input.
// A row has one retention owner; multiple dependencies must be aggregated by that
// owner before release (PayloadHeld is a latch, not a reference count).
// Retention must be set while processing with a live claim, BEFORE FinishInbox or
// FinishDelivery. A completed row cannot acquire a hold after cleanup has raced it.
// Release uses a generation-fenced transaction and only clears an existing hold;
// it never shortens PayloadRetainUntil. All methods return ErrStale on lost fences.
type PayloadRetentionStore interface {
	HoldInboxPayload(ctx context.Context, botID uint, generation uint64, id uint, claim string, retainUntil *time.Time) error
	ReleaseInboxPayload(ctx context.Context, botID uint, generation uint64, id uint) error
	HoldDeliveryPayload(ctx context.Context, botID uint, generation uint64, id uint, claim string, retainUntil *time.Time) error
	ReleaseDeliveryPayload(ctx context.Context, botID uint, generation uint64, id uint) error
}

// DeliveryRequest is the narrow module-facing envelope for durable notices.
type DeliveryRequest struct {
	BotID          uint
	Key            string
	ChatID         int64
	TelegramUserID int64
	PayloadCipher  string
	TargetType     string
	Priority       int
	Purpose        string
	Operation      string
	Status         string
	AvailableAt    time.Time
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
