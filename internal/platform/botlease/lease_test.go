package botlease

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Embed the full interface to ensure production accepts UniversalClient while
// keeping tests dependency-free. Only SET NX and EVAL are allowed here.
type fakeRedis struct {
	redis.UniversalClient
	mu      sync.Mutex
	value   string
	expires time.Time
	fail    bool
	owners  []string
}

func (f *fakeRedis) SetNX(ctx context.Context, key string, value any, ttl time.Duration) *redis.BoolCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return redis.NewBoolResult(false, errors.New("private Redis address/password"))
	}
	if ctx.Err() != nil {
		return redis.NewBoolResult(false, ctx.Err())
	}
	if f.value != "" && time.Now().Before(f.expires) {
		return redis.NewBoolResult(false, nil)
	}
	f.value = value.(string)
	f.owners = append(f.owners, f.value)
	f.expires = time.Now().Add(ttl)
	return redis.NewBoolResult(true, nil)
}

func (f *fakeRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return redis.NewCmdResult(nil, errors.New("private Redis address/password"))
	}
	if ctx.Err() != nil {
		return redis.NewCmdResult(nil, ctx.Err())
	}
	if len(keys) != 1 || len(args) == 0 {
		panic("invalid lease command")
	}
	if f.value != args[0].(string) || !time.Now().Before(f.expires) {
		return redis.NewCmdResult(int64(0), nil)
	}
	switch script {
	case renewScript:
		f.expires = time.Now().Add(time.Duration(args[1].(int64)) * time.Millisecond)
	case releaseScript:
		f.value = ""
	default:
		panic("unexpected script")
	}
	return redis.NewCmdResult(int64(1), nil)
}

func TestAcquireRenewRelease(t *testing.T) {
	ctx := context.Background()
	client := &fakeRedis{}
	lease, err := Acquire(ctx, client, "bot:42", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.value) != 64 || !lease.Valid() {
		t.Fatal("invalid owner or local lease")
	}
	if _, err := Acquire(ctx, client, "bot:42", time.Second); !errors.Is(err, ErrHeld) {
		t.Fatal(err)
	}
	if err := lease.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if lease.Valid() {
		t.Fatal("released lease remains valid")
	}
	if err := lease.Renew(ctx); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	if err := lease.Release(ctx); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	next, err := Acquire(ctx, client, "bot:42", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next.owner == lease.owner {
		t.Fatal("owner reused")
	}
}

func TestCannotRenewOrDeleteNewOwner(t *testing.T) {
	ctx := context.Background()
	client := &fakeRedis{}
	old, err := Acquire(ctx, client, "bot:42", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client.expires = time.Now().Add(-time.Second)
	next, err := Acquire(ctx, client, "bot:42", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Renew(ctx); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	if err := old.Release(ctx); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	if client.value != next.owner {
		t.Fatal("new owner removed")
	}
	if err := next.Renew(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFailClosed(t *testing.T) {
	ctx := context.Background()
	client := &fakeRedis{fail: true}
	if lease, err := Acquire(ctx, client, "bot", time.Second); lease != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatal(lease, err)
	}
	client.fail = false
	lease, err := Acquire(ctx, client, "bot", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.fail = true
	if err := lease.Renew(ctx); !errors.Is(err, ErrLost) || !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if lease.Valid() {
		t.Fatal("uncertain lease remains valid")
	}
	client.fail = false
	if err := lease.Renew(ctx); !errors.Is(err, ErrLost) {
		t.Fatal("uncertain lease revived", err)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestContextAndValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, &fakeRedis{}, "bot", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var typedNil *redis.Client
	for _, client := range []redis.UniversalClient{nil, typedNil} {
		if _, err := Acquire(context.Background(), client, "bot", time.Second); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	for _, ttl := range []time.Duration{0, -1, time.Microsecond} {
		if _, err := Acquire(context.Background(), &fakeRedis{}, "bot", ttl); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	lease, err := Acquire(context.Background(), &fakeRedis{}, "bot", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Renew(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
}

func TestLocalExpiry(t *testing.T) {
	lease, err := Acquire(context.Background(), &fakeRedis{}, "bot", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lease.deadline = time.Now().Add(-time.Second)
	if lease.Valid() {
		t.Fatal("expired lease remains valid")
	}
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
}
