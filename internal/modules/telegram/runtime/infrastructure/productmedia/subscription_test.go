package productmedia

import (
	"context"
	"errors"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"reflect"
	"testing"
)

type subscriptionFake struct {
	rt.API
	token   string
	offset  int64
	allowed []string
	err     error
}

func (f *subscriptionFake) PollWithSubscription(_ context.Context, token string, offset int64, allowed []string) ([]rt.Update, error) {
	f.token, f.offset, f.allowed = token, offset, append([]string(nil), allowed...)
	allowed[0] = "mutated"
	return []rt.Update{{ID: offset + 1, Type: "chat_member"}}, f.err
}
func TestSubscriptionForwarding(t *testing.T) {
	fake := &subscriptionFake{}
	wrapper := New(fake, "", nil)
	allowed := []string{"chat_member", "message"}
	updates, err := wrapper.PollWithSubscription(context.Background(), "test", 90, allowed)
	if err != nil || len(updates) != 1 || updates[0].ID != 91 || fake.token != "test" || fake.offset != 90 || !reflect.DeepEqual(fake.allowed, allowed) {
		t.Fatalf("forwarding %+v %v", updates, err)
	}
	fake.err = errors.New("upstream")
	if _, err = wrapper.PollWithSubscription(context.Background(), "test", 90, allowed); !errors.Is(err, fake.err) {
		t.Fatal("upstream error swallowed")
	}
	legacy := New(nil, "", nil)
	if _, err = legacy.PollWithSubscription(context.Background(), "test", 90, allowed); !errors.Is(err, rt.ErrSubscriptionUnsupported) {
		t.Fatalf("silent fallback %v", err)
	}
	if _, err = legacy.PollWithSubscription(context.Background(), "test", 90, nil); !errors.Is(err, rt.ErrInvalidSubscription) {
		t.Fatalf("empty subscription %v", err)
	}
}
