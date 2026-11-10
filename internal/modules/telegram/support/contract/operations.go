package contract

import (
	"context"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"time"
)

const (
	OperationClose          = "close"
	OperationBlock          = "block"
	OperationUnblock        = "unblock"
	OperationNote           = "note"
	OperationCallbackPrefix = "support:op:"
)

// OperationIntent carries trusted topic context and an explicit selected action.
// Note is supplied only by a dedicated internal-note UI, never parsed as a command.
type OperationIntent struct {
	Update r.Update
	Action string
	Note   string
}

// Operations is the narrow atomic staff mutation port. Implementations recheck
// persisted route, runtime generation, topic and session CAS inside one transaction.
// The application performs live membership HTTP checks before entering these calls.
type Operations interface {
	PrepareOperation(context.Context, uint, r.Update, uint64, uint64, string, string, string, time.Time) (d.OperationReference, error)
	Operation(context.Context, uint, string) (d.OperationReference, error)
	ApplyOperation(context.Context, uint, r.Update, string, time.Time) error
}

// OperationControls persists and revalidates immutable control envelopes.
// All repository checks use the transaction connection; live membership stays in application.
type OperationControls interface {
	SaveControl(context.Context, uint, r.Update, string, []string, r.Message, time.Time) (d.OperationControl, error)
	ControlForUpdate(context.Context, uint, int64) (*d.OperationControl, error)
	CheckControl(context.Context, uint, r.Message, time.Time) (d.OperationControl, error)
}

// VerificationControls reuses operation controls for daily identity-only notices.
// References have no actor until a real, live-authorized member clicks the receipt.
type VerificationControls interface {
	VerificationSessions(context.Context, uint, uint, int, time.Time) ([]d.Session, error)
	TopicIdentity(context.Context, uint, uint) (d.Session, error)
	EnsureVerificationControl(context.Context, uint, uint, uint64, time.Time) (*d.OperationControl, error)
	ApplyVerificationOperation(context.Context, uint, r.Update, string, uint64, time.Time) error
}

const OperationControlPurpose = "support_control"

// ValidOperation returns whether action is a supported explicit staff action.
func ValidOperation(action string) bool {
	return action == OperationClose || action == OperationBlock || action == OperationUnblock || action == OperationNote
}
