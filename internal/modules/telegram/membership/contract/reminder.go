package contract

import (
	"context"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	"time"
)

// ReminderRepository persists fair scans and immutable notification identities on one DB connection.
type ReminderRepository interface {
	ScanReminders(context.Context, uint, int, time.Time) (int, error)
	DueReminders(context.Context, uint, int, time.Time) ([]d.Reminder, error)
	Reminder(context.Context, uint) (*d.Reminder, error)
	FinishReminder(context.Context, uint, string, int64, time.Time) error
	Eligibility(context.Context, AccessIdentity, uint, time.Time) (AccessEligibility, error)
}

// ProactiveRepository derives bounded work and uses lease-owner CAS for every transition.
// Scanning only derives responsibilities; it never grants, pays, or sends a message.
type ProactiveRepository interface {
	ScanProactive(context.Context, uint, int, time.Time) (int, error)
	ClaimProactive(context.Context, uint, string, time.Time, time.Duration) (*d.ProactiveDelivery, error)
	SaveProactive(context.Context, *d.ProactiveDelivery, time.Time) error
	Proactive(context.Context, uint) (*d.ProactiveDelivery, error)
	ValidateProactive(context.Context, *d.ProactiveDelivery, time.Time) error
}
