package contract

import (
	"context"
	"time"
)

// DeliveryDeferred asks runtime to retain a definitely unsent message.
// Transient distinguishes dependency failures from policy boundaries (zero value).
// Policy delays do not consume the durable delivery failure budget.
type DeliveryDeferred struct {
	AvailableAt time.Time
	Transient   bool
}

// DeliveryPolicyDeferred is the safe persistence marker for an unspent claim.
const DeliveryPolicyDeferred = "delivery deferred by policy"

// As accepts both historical value consumers and pointer consumers, including
// wrapped errors. target is supplied by errors.As; false means an unrelated type.
func (deferred DeliveryDeferred) As(target any) bool {
	switch destination := target.(type) {
	case *DeliveryDeferred:
		*destination = deferred
	case **DeliveryDeferred:
		copy := deferred
		*destination = &copy
	default:
		return false
	}
	return true
}

// DeliveryInvalidated classifies a definitely unsent stale or unreachable reminder.
type DeliveryInvalidated struct{ State string }

// As preserves pointer/value compatibility for stale-delivery consumers.
// target is the errors.As destination; false leaves unrelated types untouched.
func (invalid DeliveryInvalidated) As(target any) bool {
	switch destination := target.(type) {
	case *DeliveryInvalidated:
		*destination = invalid
	case **DeliveryInvalidated:
		copy := invalid
		*destination = &copy
	default:
		return false
	}
	return true
}

// Error returns only a safe category, never identity or invitation data.
func (DeliveryInvalidated) Error() string { return "delivery authorization invalidated" }

// Error returns a secret-free policy category.
func (DeliveryDeferred) Error() string { return "delivery deferred by policy" }

// ReminderDeliveryRef identifies a durable membership reminder, never an invitation.
type ReminderDeliveryRef struct {
	ID uint `json:"id"`
}

// ReminderDeliveryAuthorizer revalidates expiry, cycle, binding, route and runtime fence before HTTP.
type ReminderDeliveryAuthorizer interface {
	AuthorizeReminderDelivery(context.Context, uint, Message) error
}

// JoinAbuseDeliveryRef refers to durable confirmed-decline evidence, never a link.
type JoinAbuseDeliveryRef struct {
	ID         uint   `json:"id"`
	Generation uint64 `json:"generation"`
}

// JoinAbuseDeliveryAuthorizer rechecks the original owner and evidence before HTTP.
type JoinAbuseDeliveryAuthorizer interface {
	AuthorizeJoinAbuseDelivery(context.Context, uint, Message) error
}

// BusinessDeliveryReader reads real queue evidence by its persistent idempotency key.
type BusinessDeliveryReader interface {
	BusinessDelivery(context.Context, uint, string) (InviteDeliveryStatus, error)
}
