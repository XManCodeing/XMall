package botlease

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// startIsolatedRedis never reads project configuration, credentials or an
// existing Redis instance. Port zero is only used to reserve a free TCP port;
// redis-server gets the actual port (Redis's --port 0 disables TCP).
func startIsolatedRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("real Redis integration skipped: redis-server binary not available")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := exec.Command(binary,
		"--bind", "127.0.0.1", "--port", strconv.Itoa(port),
		"--protected-mode", "yes", "--save", "", "--appendonly", "no",
		"--daemonize", "no", "--dir", dir, "--loglevel", "warning")
	command.Dir = dir
	command.Env = []string{"HOME=" + dir, "LC_ALL=C"}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("cannot start isolated redis-server: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("isolated redis-server did not exit")
			}
		})
	}
	t.Cleanup(stop)
	client := redis.NewClient(&redis.Options{
		Addr:       net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		MaxRetries: -1, DialTimeout: 200 * time.Millisecond,
		ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := client.Ping(ctx).Err(); err == nil {
			// If the child exited (e.g. port collision), never test another server.
			select {
			case <-done:
				once.Do(func() {})
				t.Fatal("isolated redis-server exited during startup")
			default:
			}
			info, err := client.Info(ctx, "server").Result()
			if err != nil || !strings.Contains(info, "\r\nprocess_id:"+strconv.Itoa(command.Process.Pid)+"\r\n") {
				t.Fatal("Redis readiness endpoint does not belong to the isolated child")
			}
			t.Log("executing against isolated real redis-server on loopback")
			return client, stop
		}
		select {
		case <-done:
			once.Do(func() {})
			t.Fatal("isolated redis-server exited before readiness")
		case <-ctx.Done():
			t.Fatal("isolated redis-server readiness timeout")
		case <-ticker.C:
		}
	}
}

func TestRedisIntegration(t *testing.T) {
	client, stop := startIsolatedRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("concurrent owners and renewal", func(t *testing.T) {
		const competitors = 16
		type outcome struct {
			lease *Lease
			err   error
		}
		results := make(chan outcome, competitors)
		start := make(chan struct{})
		for range competitors {
			go func() {
				<-start
				lease, err := Acquire(ctx, client, "integration:competition", 3*time.Second)
				results <- outcome{lease, err}
			}()
		}
		close(start)
		var winner *Lease
		wins := 0
		for range competitors {
			result := <-results
			if result.err == nil {
				wins++
				winner = result.lease
			} else if !errors.Is(result.err, ErrHeld) {
				t.Fatalf("unexpected acquisition error: %v", result.err)
			}
		}
		if wins != 1 {
			t.Fatalf("got %d leaders, want exactly one", wins)
		}
		// Shorten Redis's expiry, then prove the production Lua restores it.
		if err := client.PExpire(ctx, winner.key, time.Second).Err(); err != nil {
			t.Fatal(err)
		}
		if err := winner.Renew(ctx); err != nil {
			t.Fatal(err)
		}
		remaining, err := client.PTTL(ctx, winner.key).Result()
		if err != nil || remaining <= time.Second || remaining > 3*time.Second {
			t.Fatalf("renewed TTL %v: %v", remaining, err)
		}
		if err := winner.Release(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expiry reacquire and stale owner isolation", func(t *testing.T) {
		old, err := Acquire(ctx, client, "integration:expiry", 150*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		expiryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			exists, err := client.Exists(expiryCtx, old.key).Result()
			if err != nil {
				t.Fatal(err)
			}
			if exists == 0 {
				break
			}
			select {
			case <-ticker.C:
			case <-expiryCtx.Done():
				t.Fatal("lease failed to expire")
			}
		}
		next, err := Acquire(ctx, client, old.key, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if old.owner == next.owner {
			t.Fatal("owner was reused")
		}
		if err := old.Release(ctx); !errors.Is(err, ErrLost) {
			t.Fatalf("stale release: %v", err)
		}
		if owner, err := client.Get(ctx, next.key).Result(); err != nil || owner != next.owner {
			t.Fatal("stale owner deleted successor", err)
		}
		if err := old.Renew(ctx); !errors.Is(err, ErrLost) {
			t.Fatalf("stale renewal: %v", err)
		}
		if err := next.Renew(ctx); err != nil {
			t.Fatal(err)
		}
		if err := next.Release(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disconnected renewal fails closed", func(t *testing.T) {
		lease, err := Acquire(ctx, client, "integration:outage", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		stop() // Real transport failure, not a mocked Redis response.
		err = lease.Renew(ctx)
		if !errors.Is(err, ErrLost) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("renewal did not fail closed: %v", err)
		}
		if lease.Valid() {
			t.Fatal("uncertain lease remains valid")
		}
		if err := lease.Renew(ctx); !errors.Is(err, ErrLost) {
			t.Fatalf("invalid handle revived: %v", err)
		}
		if next, err := Acquire(ctx, client, "integration:outage", time.Second); next != nil || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("acquired during outage: %v", err)
		}
	})
}
