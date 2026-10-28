package selfupdate

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const restartDelay = 500 * time.Millisecond

// RestartExitCode is deliberately nonzero: systemd Restart=on-failure must
// relaunch this exit, unlike an ordinary SIGTERM / systemctl stop.
const RestartExitCode = 70

// RestartMethod is independent of Supervisor: exec does not add a watchdog.
type RestartMethod string

const (
	RestartNone    RestartMethod = "none"
	RestartSystemd RestartMethod = "systemd"
	RestartExec    RestartMethod = "exec"
)

// Capture the launch context before configuration or updates can change it.
// Never reconstruct argv from a shell string, or log the environment.
var launchArgs = append([]string(nil), os.Args...)
var launchEnv = append([]string(nil), os.Environ()...)
var launchCWD, launchCWDErr = os.Getwd()
var launchCWDInfo, launchCWDStatErr = os.Stat(launchCWD)
var pendingRestart atomic.Pointer[RestartPlan]
var restartReserved atomic.Bool

// RestartPlan owns the manager reservation and cross-process binary lock from
// preflight until exec/exit. os.OpenFile makes the lock FD close-on-exec: keeping
// it locked until the kernel commits exec avoids an unlock/rename race, without
// inheriting a lock that would deadlock the new process's StartupGuard.
type RestartPlan struct {
	method      RestartMethod
	path        string
	release     func()
	requestOnce sync.Once
	finishOnce  sync.Once
}

func RestartRequested() bool { return pendingRestart.Load() != nil }

// PrepareRestart serializes restart with updates, rollback and other restarts.
// It does not signal or stop anything; the HTTP caller must first send/flush its
// response, then call Request. Cancellation before Request leaves service live.
func (m *Manager) PrepareRestart() (*RestartPlan, error) {
	if err := m.acquire(); err != nil {
		return nil, err
	}
	p := &RestartPlan{release: m.releaseBusy}
	if !restartReserved.CompareAndSwap(false, true) {
		m.releaseBusy()
		return nil, ErrUpdateInProgress
	}
	fail := func(err error) (*RestartPlan, error) {
		p.Cancel()
		return nil, err
	}
	c := m.detect()
	if !c.CanRestart || (c.RestartMethod != RestartSystemd && c.RestartMethod != RestartExec) {
		return fail(ErrNotSupported)
	}
	p.method, p.path = c.RestartMethod, c.ExecPath
	if p.path == "" {
		return fail(ErrNotSupported)
	}
	unlock, err := acquireBinaryLock(p.path)
	if err != nil {
		return fail(err)
	}
	p.release = func() { unlock(); m.releaseBusy() }
	// Recheck the selected target under the same lock used by CLI rollback.
	// systemd keeps its existing policy; exec never bypasses a denied policy.
	if p.method == RestartExec {
		if err := preflightExec(p.path); err != nil {
			return fail(fmt.Errorf("restart preflight: %w", err))
		}
	}
	return p, nil
}

// Cancel is only for a plan whose shutdown has not been requested.
func (p *RestartPlan) Cancel() {
	p.finishOnce.Do(func() {
		pendingRestart.CompareAndSwap(p, nil)
		p.release()
		restartReserved.Store(false)
	})
}

// Request begins graceful shutdown after the response has been flushed. The
// delay also lets request middleware finish; HTTP Shutdown drains requests.
func (p *RestartPlan) Request() {
	p.requestOnce.Do(func() {
		pendingRestart.Store(p)
		go func() {
			time.Sleep(restartDelay)
			process, err := os.FindProcess(os.Getpid())
			if err == nil {
				err = process.Signal(syscall.SIGTERM)
			}
			if err != nil {
				// No hard-exit fallback: a failed signal must not abruptly kill
				// an otherwise working service (especially without a supervisor).
				fmt.Fprintf(os.Stderr, "self-update shutdown signal failed; service not restarted: %v\n", err)
				p.Cancel()
			}
		}()
	})
}

// FinishRestart runs ONLY after Runner.Stop and resource cleanup succeeded.
// Failure strategy: preflight errors before shutdown leave the old service live;
// errors here require an operator/panel restart and a nonzero exit. Successful
// exec replaces the old process irreversibly. A subsequent startup/migration
// failure cannot restore its old memory or service. Never auto-rollback the
// binary/database: migration may have partially advanced the schema.
func FinishRestart() error {
	p := pendingRestart.Load()
	if p == nil {
		return nil
	}
	if p.method == RestartSystemd {
		// Keep the lock until process exit, not until this function returns.
		return nil
	}
	defer p.Cancel() // exec only returns on failure
	if err := preflightExec(p.path); err != nil {
		return fmt.Errorf("post-shutdown preflight failed; service stopped, manual restart required: %w", err)
	}
	if err := os.Chdir(launchCWD); err != nil {
		return fmt.Errorf("restore launch working directory: %w", err)
	}
	if err := execProcess(p.path, launchArgs, launchEnv); err != nil {
		return fmt.Errorf("exec failed; service stopped, manual restart required (no automatic rollback): %w", err)
	}
	return nil
}
