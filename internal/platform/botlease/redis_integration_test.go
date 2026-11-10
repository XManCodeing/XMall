package botlease

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// startIsolatedRedis starts a disposable child and returns its client, idempotent
// stop function and port. Pass zero to select a random loopback port, or the
// stopped child's port to restart there. No project configuration is read.
func startIsolatedRedis(t *testing.T, port int) (*redis.Client, func(), int) {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("real Redis integration skipped: redis-server binary not available")
	}
	// Reserve even a restart port so an occupied endpoint is rejected before
	// starting a child. Readiness additionally verifies the child's PID.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("cannot reserve isolated Redis port: %v", err)
	}
	port = listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("cannot release isolated Redis port reservation: %v", err)
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
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cannot kill isolated redis-server: %v", err)
			}
			select {
			case err := <-done:
				var exitErr *exec.ExitError
				if err != nil && !errors.As(err, &exitErr) {
					t.Errorf("cannot reap isolated redis-server: %v", err)
				}
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
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("cannot close isolated Redis client: %v", err)
		}
	})
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
			return client, stop, port
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

// TestRedisIntegration runs ownership checks against an isolated real child.
// t receives failures; this test has no return value.
func TestRedisIntegration(t *testing.T) {
	client, stop, _ := startIsolatedRedis(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Run("concurrent owners and renewal", func(t *testing.T) {
		testRedisConcurrentOwners(t, ctx, client)
	})
	t.Run("expiry reacquire and stale owner isolation", func(t *testing.T) {
		testRedisExpiredOwner(t, ctx, client)
	})
	t.Run("disconnected renewal fails closed", func(t *testing.T) {
		testRedisDisconnectedRenewal(t, ctx, client, stop)
	})
}

// testRedisConcurrentOwners uses ctx and client to verify contention and renewal;
// failures are reported through t and no value is returned.
func testRedisConcurrentOwners(t *testing.T, ctx context.Context, client *redis.Client) {
	t.Helper()
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
}

// testRedisExpiredOwner uses ctx and client to check successor isolation after
// expiry; failures are reported through t and no value is returned.
func testRedisExpiredOwner(t *testing.T, ctx context.Context, client *redis.Client) {
	t.Helper()
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
}

// testRedisDisconnectedRenewal calls stop to kill client's server and verifies
// fail-closed behavior using ctx. It reports failures through t, returning nothing.
func testRedisDisconnectedRenewal(t *testing.T, ctx context.Context, client *redis.Client, stop func()) {
	t.Helper()
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
}

// TestRedisIntegrationRestart verifies real same-port restart isolation for both
// an invalidated handle and handles that never observed the outage. t reports
// failures; the test returns no value and skips without redis-server in PATH.
func TestRedisIntegrationRestart(t *testing.T) {
	// Skip the parent too, so an all-skipped scenario list is not shown as PASS.
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("real Redis restart integration skipped: redis-server binary not available")
	}
	for _, scenario := range []struct {
		name          string
		observeOutage bool
		releaseFirst  bool
	}{
		{name: "invalidated by disconnected renewal", observeOutage: true},
		{name: "unobserved outage renewal first"},
		{name: "unobserved outage release first", releaseFirst: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client, stop, port := startIsolatedRedis(t, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			old, err := Acquire(ctx, client, "integration:restart", time.Minute)
			if err != nil {
				t.Fatalf("acquire before restart: %v", err)
			}
			stop() // Kill and reap the child before reusing its port; do not close client.
			if scenario.observeOutage {
				if err := old.Renew(ctx); !errors.Is(err, ErrLost) || !errors.Is(err, ErrUnavailable) {
					t.Fatalf("disconnected renewal must lose the lease: %v", err)
				}
				if old.Valid() {
					t.Fatal("disconnected renewal left the old handle valid")
				}
			}
			restarted, _, restartedPort := startIsolatedRedis(t, port)
			if restartedPort != port || restarted.Options().Addr != client.Options().Addr {
				t.Fatal("redis-server did not restart on the original endpoint")
			}
			// The original client must reconnect successfully, not just return a
			// transport error that could disguise stale-owner checks.
			if err := client.Ping(ctx).Err(); err != nil {
				t.Fatalf("original client cannot reconnect after restart: %v", err)
			}
			if exists, err := restarted.Exists(ctx, old.key).Result(); err != nil || exists != 0 {
				t.Fatalf("restart retained the old key: exists=%d error=%v", exists, err)
			}
			next, err := Acquire(ctx, restarted, old.key, 30*time.Second)
			if err != nil {
				t.Fatalf("successor cannot acquire after restart: %v", err)
			}
			if old.owner == next.owner {
				t.Fatal("restart reused the previous owner")
			}
			if scenario.observeOutage && old.Valid() {
				t.Fatal("restart revived an invalidated handle")
			}
			if !scenario.observeOutage && !old.Valid() {
				t.Fatal("unobserved handle expired locally before ownership was checked")
			}
			assertRestartRejectsOldOwner(t, ctx, restarted, old, next, scenario.releaseFirst)
		})
	}
}

// assertRestartRejectsOldOwner checks old against next using ctx and client;
// releaseFirst chooses the first stale operation. Failures go to t; no value is
// returned. TTL and owner checks detect both accidental renewal and deletion.
func assertRestartRejectsOldOwner(t *testing.T, ctx context.Context, client *redis.Client, old, next *Lease, releaseFirst bool) {
	t.Helper()
	// A short successor TTL, distinct from old.ttl, makes accidental renewal
	// observable without sleeping or relying on millisecond-exact timing.
	const successorTTL = 10 * time.Second
	if err := client.PExpire(ctx, next.key, successorTTL).Err(); err != nil {
		t.Fatalf("shorten successor TTL: %v", err)
	}
	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "renew", run: old.Renew},
		{name: "release", run: old.Release},
		{name: "renew again", run: old.Renew},
	}
	if releaseFirst {
		operations[0], operations[1] = operations[1], operations[0]
	}
	for _, operation := range operations {
		if err := operation.run(ctx); !errors.Is(err, ErrLost) || errors.Is(err, ErrUnavailable) {
			t.Fatalf("stale %s must reject ownership without a transport error: %v", operation.name, err)
		}
		if old.Valid() {
			t.Fatalf("stale %s revived the old handle", operation.name)
		}
		if owner, err := client.Get(ctx, next.key).Result(); err != nil || owner != next.owner {
			t.Fatalf("stale %s changed successor ownership: %v", operation.name, err)
		}
		if remaining, err := client.PTTL(ctx, next.key).Result(); err != nil || remaining <= 0 || remaining > successorTTL {
			t.Fatalf("stale %s changed successor TTL: ttl=%v error=%v", operation.name, remaining, err)
		}
	}
	if err := next.Renew(ctx); err != nil {
		t.Fatalf("successor cannot renew after stale operations: %v", err)
	}
	if err := next.Release(ctx); err != nil {
		t.Fatalf("successor cannot release after stale operations: %v", err)
	}
	if exists, err := client.Exists(ctx, next.key).Result(); err != nil || exists != 0 {
		t.Fatalf("successor release did not remove its key: exists=%d error=%v", exists, err)
	}
}
