package contract

import (
	"context"
	"errors"
)

// Delivery priorities are server-owned: transactions precede support, then marketing.
const (
	PriorityTransaction = 200
	PrioritySupport     = 80
	PriorityMarketing   = 1
)

type TargetType string

const (
	TargetShopPrivate     TargetType = "shop_private"
	TargetSupportPrivate  TargetType = "support_private"
	TargetSupportGroup    TargetType = "support_group"
	TargetMemberOperation TargetType = "member_operation"
)

// Normalize preserves unknown values so they cannot acquire legacy privileges.
func (t TargetType) Normalize() TargetType {
	if t == "" {
		return TargetShopPrivate
	}
	return t
}
func (t TargetType) Valid() bool {
	switch t.Normalize() {
	case TargetShopPrivate, TargetSupportPrivate, TargetSupportGroup, TargetMemberOperation:
		return true
	default:
		return false
	}
}

// TargetAuthorizer is optional and does not replace the legacy Handler contract.
// Implementations must verify persisted business references and current route versions.
type TargetAuthorizer interface {
	AuthorizeTarget(context.Context, uint, Message) error
}

type SubscriptionAPI interface {
	PollWithSubscription(context.Context, string, int64, []string) ([]Update, error)
}

var ErrInvalidSubscription = errors.New("telegram update subscription must be a nonempty explicit whitelist")
var ErrSubscriptionUnsupported = errors.New("telegram update subscription unavailable")

// ValidateSubscription accepts only update types supported by this runtime adapter.
// Empty, padded, unknown and duplicate entries fail closed rather than reset Telegram's filter.
func ValidateSubscription(types []string) error {
	if len(types) == 0 {
		return ErrInvalidSubscription
	}
	seen := make(map[string]bool, len(types))
	for _, typ := range types {
		switch typ {
		case "message", "edited_message", "channel_post", "edited_channel_post", "callback_query", "my_chat_member", "chat_member", "chat_join_request":
		default:
			return ErrInvalidSubscription
		}
		if seen[typ] {
			return ErrInvalidSubscription
		}
		seen[typ] = true
	}
	return nil
}
