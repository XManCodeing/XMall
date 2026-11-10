package application

import (
	"context"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// TopicDeletionAuthorizer requires the exact POST grant independently of general recovery.
type TopicDeletionAuthorizer interface {
	AuthorizeSupportTopicDeletion(context.Context, uint, uint) error
}

// ConfirmTopicDeleted checks backend resolution authority on every attempt.
// bot/topic and generation must be loaded by the trusted admin adapter; in is an
// explicit physical deletion confirmation, never inferred from a transport error.
// It returns the durable CAS/audit result and never creates a replacement itself.
func (resolver *Resolver) ConfirmTopicDeleted(ctx context.Context, bot, topic uint, in c.TopicDeletionConfirmation, now time.Time) error {
	if resolver == nil || resolver.Authorizer == nil || resolver.Topics == nil || in.ActorID == 0 ||
		in.Generation == 0 || !adminEvidenceReference.MatchString(in.Evidence) {
		return c.ErrDenied
	}
	deletions, ok := resolver.Topics.(c.TopicDeletions)
	if !ok {
		return c.ErrDenied
	}
	authority, allowed := resolver.Authorizer.(TopicDeletionAuthorizer)
	if !allowed {
		return c.ErrDenied
	}
	if err := authority.AuthorizeSupportTopicDeletion(ctx, in.ActorID, bot); err != nil {
		return err
	}
	err := deletions.ConfirmTopicDeleted(ctx, bot, topic, in, now)
	logRecovery(in.ActorID, topic, "topic_deleted", err)
	return err
}
