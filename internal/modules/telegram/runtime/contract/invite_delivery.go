package contract

import "context"

// InviteDeliveryRef binds an encrypted panel to one existing invite and current authorization.
// It contains no link and is internal-only; runtime keys retain the association after payload cleanup.
type InviteDeliveryRef struct {
	InviteID, TargetID, BindingVersion           uint
	RouteVersion, EntitlementVersion, Generation uint64
}

// InviteDeliveryAuthorizer rechecks current binding, route and eligibility before sending.
type InviteDeliveryAuthorizer interface {
	AuthorizeInviteDelivery(context.Context, uint, Message) error
}

// InviteDeliveryStatus is a secret-free observation of the existing runtime queue.
type InviteDeliveryStatus struct {
	Operation string `json:"operation"`
	State     string `json:"state"`
	MessageID int64  `json:"message_id,string,omitempty"`
}

// InviteDeliveryReader reads the latest delivery; not_requested means no Bot enqueue exists.
type InviteDeliveryReader interface {
	InviteDelivery(context.Context, uint, uint) (InviteDeliveryStatus, error)
}
