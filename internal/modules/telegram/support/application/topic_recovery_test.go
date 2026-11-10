package application

import (
	"context"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// deletionTestStore records calls behind the application's authorization boundary.
type deletionTestStore struct {
	c.Topics
	calls int
}

// ConfirmTopicDeleted records one test invocation without external side effects.
func (store *deletionTestStore) ConfirmTopicDeleted(context.Context, uint, uint, c.TopicDeletionConfirmation, time.Time) error {
	store.calls++
	return nil
}

// AuthorizeSupportTopicDeletion mirrors revocation for the exact deletion authority.
func (authority *adminResolutionFixture) AuthorizeSupportTopicDeletion(ctx context.Context, actor, bot uint) error {
	return authority.AuthorizeSupportResolution(ctx, actor, bot)
}

// TestTopicDeletionRequiresFreshAdminAuthority rejects untrusted evidence and
// repeats authorization for every invocation, including a now-revoked actor.
func TestTopicDeletionRequiresFreshAdminAuthority(t *testing.T) {
	store := &deletionTestStore{}
	authority := &adminResolutionFixture{}
	resolver := Resolver{Topics: store, Authorizer: authority}
	in := c.TopicDeletionConfirmation{ActorID: 9, Generation: 1, Evidence: "ticket:123"}
	if err := resolver.ConfirmTopicDeleted(context.Background(), 2, 3, in, time.Now()); err != nil {
		t.Fatal(err)
	}
	authority.denied = true
	if err := resolver.ConfirmTopicDeleted(context.Background(), 2, 3, in, time.Now()); err == nil {
		t.Fatal("revoked authority accepted")
	}
	in.Evidence = "https://untrusted.example/raw-body"
	if err := resolver.ConfirmTopicDeleted(context.Background(), 2, 3, in, time.Now()); err == nil {
		t.Fatal("raw evidence accepted")
	}
	if store.calls != 1 || authority.calls != 2 {
		t.Fatal(store.calls, authority.calls)
	}
}
