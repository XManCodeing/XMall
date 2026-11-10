package contract

import (
	"context"
	"errors"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	"time"
)

var (
	ErrChatUnresolved = errors.New("telegram group ID could not be verified")
	ErrChatAmbiguous  = errors.New("telegram group ID matches multiple groups")
	ErrConflict       = errors.New("telegram group version or occupancy conflict")
	ErrNotFound       = errors.New("telegram group not found")
	ErrInvalid        = errors.New("invalid telegram group configuration")
	ErrCapability     = errors.New("telegram group capability unavailable")
	ErrBusy           = errors.New("telegram group check pending or cooling down")
)

// Input describes a target draft. Name is editable for existing targets only;
// creation ignores it and obtains the exact title from Telegram.
type Input struct {
	Name                 string `json:"name"`
	Purpose              string `json:"purpose"`
	BotInstanceID        uint   `json:"bot_instance_id"`
	TelegramChatID       string `json:"telegram_chat_id"`
	Enabled              bool   `json:"enabled"`
	ExpectedVersion      uint64 `json:"expected_version"`
	ExpectedRouteVersion uint64 `json:"expected_route_version"`
}
type ModuleInput struct {
	Partition       string `json:"partition"`
	ExpectedVersion uint64 `json:"expected_version"`
	Enabled         bool   `json:"enabled"`
	TargetID        uint   `json:"target_id"`
}

// ModuleView separates saved intent from read-time capability gating. Enabled
// remains the effective flag for existing consumers; ConfiguredEnabled is not
// authorization and is only suitable for editing the saved configuration.
type ModuleView struct {
	d.ModuleConfig
	ConfiguredEnabled bool `json:"configured_enabled"`
	EffectiveEnabled  bool `json:"effective_enabled"`
}

type View struct {
	d.Target
	Fresh               bool       `json:"fresh"`
	FreshUntil          *time.Time `json:"fresh_until"`
	MissingCapabilities []string   `json:"missing_capabilities"`
	AffectedFunctions   []string   `json:"affected_functions"`
	Checking            bool       `json:"checking"`
}
type Detail struct {
	View
	// EnableRejected reports a saved disabled result after a fresh enable check failed.
	EnableRejected bool          `json:"enable_rejected,omitempty"`
	Routes         []d.Route     `json:"routes"`
	Operations     []d.Operation `json:"operations"`
}
type Filter struct {
	BotID          uint
	Purpose        string
	ChatID         int64 // Optional exact physical route for trusted platform updates.
	Page, PageSize int
}

// CheckedStore atomically persists a server-probed target against source observation CAS.
// The observation is produced inside application code, never accepted from HTTP input.
type CheckedStore interface {
	SaveChecked(context.Context, *d.Target, uint64, uint64, uint64) error
}

type Store interface {
	Get(context.Context, uint) (*d.Target, error)
	List(context.Context, Filter) ([]d.Target, int64, error)
	Save(context.Context, *d.Target, uint64, uint64) error
	History(context.Context, uint) ([]d.Route, []d.Operation, error)
	BeginCheck(context.Context, uint, uint64, string, uint, time.Time) (*d.Target, *d.Operation, error)
	FinishCheck(context.Context, *d.Operation, d.Observation, time.Time) (bool, error)
	Due(context.Context, uint, uint64, time.Time, int) ([]d.Target, error)
	Invalidate(context.Context, uint, int64, time.Time) error
	Modules(context.Context, uint) ([]ModuleView, error)
	SaveModule(context.Context, *d.ModuleConfig, uint64) error
}

// InstanceAccess never returns credentials to application or transport callers.
type Identity struct {
	TelegramID int64
	Generation uint64
	Running    bool
}
type ProbeResult struct {
	Observation d.Observation
	ErrorClass  string
	RetryAfter  int
}
type Probe interface {
	Instance(context.Context, uint) (Identity, error)
	Check(context.Context, uint, int64) ProbeResult
}
