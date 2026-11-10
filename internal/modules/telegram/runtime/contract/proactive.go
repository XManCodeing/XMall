package contract

import "context"

// ProactiveDeliveryRef binds a message to a persisted grant responsibility and a fixed feedback stage.
type ProactiveDeliveryRef struct {
	ID    uint   `json:"id"`
	Stage string `json:"stage"`
}

// ProactiveDeliveryAuthorizer validates independent background delivery without a shopping session.
// Implementations must still verify current contact, identity stamp, binding, route and generation.
type ProactiveDeliveryAuthorizer interface {
	AuthorizeProactiveDelivery(context.Context, uint, Message) error
}
