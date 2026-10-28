// Package botlease provides Redis-backed, owner-checked bot leadership leases.
// It does not fall back to an in-process lock when Redis is unavailable.
package botlease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrHeld        = errors.New("botlease: lease already held")
	ErrLost        = errors.New("botlease: lease lost")
	ErrUnavailable = errors.New("botlease: Redis unavailable")
	ErrInvalid     = errors.New("botlease: invalid client, key or TTL")
)

const renewScript = `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) else return 0 end`
const releaseScript = `if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) else return 0 end`

// Lease must not be copied. Runtime must stop polling/sending on ANY Renew
// error, not only ErrLost. A lease is not a fencing token for external effects.
// Redis clients should enable ContextTimeoutEnabled and bounded I/O timeouts.
type Lease struct {
	mu       sync.Mutex
	client   redis.UniversalClient
	key      string
	owner    string
	ttl      time.Duration
	deadline time.Time
	lost     bool
}

// Acquire uses a cryptographically random owner and SET NX with a TTL. The key
// should identify a bot without containing its token. TTL must be >= 1ms.
// Contention returns ErrHeld; infrastructure errors return ErrUnavailable.
func Acquire(ctx context.Context, client redis.UniversalClient, key string, ttl time.Duration) (*Lease, error) {
	if client == nil || (reflect.ValueOf(client).Kind() == reflect.Ptr && reflect.ValueOf(client).IsNil()) || key == "" || ttl < time.Millisecond {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var owner [32]byte
	if _, err := rand.Read(owner[:]); err != nil {
		return nil, errors.New("botlease: owner generation failed")
	}
	// Round down to Redis's millisecond precision, never extending locally.
	ttl = ttl.Truncate(time.Millisecond)
	started := time.Now()
	lease := &Lease{client: client, key: key, owner: hex.EncodeToString(owner[:]), ttl: ttl, deadline: started.Add(ttl)}
	ok, err := client.SetNX(ctx, key, lease.owner, ttl).Result()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if !ok {
		return nil, ErrHeld
	}
	if !time.Now().Before(lease.deadline) {
		return nil, ErrLost
	}
	return lease, nil
}

// Renew extends only this owner's lease. Any uncertain outcome permanently
// invalidates this handle; reacquire instead of reviving a possibly lost lease.
func (l *Lease) Renew(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lost || !time.Now().Before(l.deadline) {
		l.lost = true
		return ErrLost
	}
	if err := ctx.Err(); err != nil {
		l.lost = true
		return errors.Join(ErrLost, err)
	}
	started := time.Now()
	result, err := l.client.Eval(ctx, renewScript, []string{l.key}, l.owner, l.ttl.Milliseconds()).Int64()
	if ctx.Err() != nil {
		l.lost = true
		return errors.Join(ErrLost, ctx.Err())
	}
	if err != nil {
		l.lost = true
		return errors.Join(ErrLost, ErrUnavailable)
	}
	if result != 1 || !time.Now().Before(started.Add(l.ttl)) {
		l.lost = true
		return ErrLost
	}
	l.deadline = started.Add(l.ttl)
	return nil
}

// Release conditionally deletes only this owner's key. The handle is invalid
// afterwards even if Redis fails. Release may also clean up an uncertain Renew.
func (l *Lease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lost = true
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrLost, err)
	}
	result, err := l.client.Eval(ctx, releaseScript, []string{l.key}, l.owner).Int64()
	if ctx.Err() != nil {
		return errors.Join(ErrLost, ctx.Err())
	}
	if err != nil {
		return errors.Join(ErrLost, ErrUnavailable)
	}
	if result != 1 {
		return ErrLost
	}
	return nil
}

// Valid is a conservative local check, NOT a Redis ownership check. Runtime
// must renew well before expiry and stop work when Valid becomes false.
func (l *Lease) Valid() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.lost && time.Now().Before(l.deadline)
}
