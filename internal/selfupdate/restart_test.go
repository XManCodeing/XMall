package selfupdate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRestartSelection(t *testing.T) {
	for _, tc := range []struct {
		name, goos   string
		supervisor   Supervisor
		policy       string
		known        bool
		preflightErr error
		method       RestartMethod
		reason       string
	}{
		{"systemd always", "linux", SupervisorSystemd, "always", true, nil, RestartSystemd, ""},
		{"systemd disabled never bypassed", "linux", SupervisorSystemd, "no", true, nil, RestartNone, "systemd_policy_disabled"},
		{"systemd unknown never bypassed", "linux", SupervisorSystemd, "", false, nil, RestartNone, "systemd_policy_unknown"},
		{"native linux", "linux", SupervisorNone, "", false, nil, RestartExec, ""},
		{"native linux invalid", "linux", SupervisorNone, "", false, errors.New("bad executable"), RestartNone, "exec_preflight_failed"},
		{"darwin unchanged", "darwin", SupervisorNone, "", false, nil, RestartNone, "unsupported_os"},
		{"windows unchanged", "windows", SupervisorNone, "", false, nil, RestartNone, "unsupported_os"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Capability{Supervisor: tc.supervisor}
			configureRestart(&c, tc.goos, 100, func() (unitRestartConfig, bool) { return unitRestartConfig{MainPID: 100, Restart: tc.policy}, tc.known }, func(string) error {
				if tc.supervisor == SupervisorSystemd {
					t.Fatal("systemd must not fall back to exec")
				}
				return tc.preflightErr
			})
			if c.RestartMethod != tc.method || c.RestartBlockReason != tc.reason || c.CanRestart != (tc.method != RestartNone) {
				t.Fatalf("unexpected capability: %+v", c)
			}
		})
	}
	for _, cfg := range []unitRestartConfig{
		{Restart: "always", RestartPreventExitStatus: map[int]bool{RestartExitCode: true}},
		{Restart: "on-failure", SuccessExitStatus: map[int]bool{RestartExitCode: true}},
	} {
		c := Capability{Supervisor: SupervisorSystemd}
		cfg.MainPID = 100
		configureRestart(&c, "linux", 100, func() (unitRestartConfig, bool) { return cfg, true }, func(string) error { t.Fatal("exec bypass"); return nil })
		if c.CanRestart {
			t.Fatal("explicit systemd exit restrictions ignored")
		}
	}
}

func TestRestartSelectionForPanelChildren(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		mainPID      int
		known        bool
		preflightErr error
		method       RestartMethod
		reason       string
	}{
		{"panel no restart", "no", 50, true, nil, RestartExec, ""},
		{"panel always must not exit", "always", 50, true, nil, RestartExec, ""},
		{"panel bad executable", "no", 50, true, errors.New("invalid ELF"), RestartNone, "exec_preflight_failed"},
		{"main process disabled", "no", 100, true, nil, RestartNone, "systemd_policy_disabled"},
		{"unknown owner", "no", 0, true, nil, RestartNone, "systemd_policy_unknown"},
		{"query failed", "no", 50, false, nil, RestartNone, "systemd_policy_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Capability{Supervisor: SupervisorSystemd, ExecPath: "/opt/xmall", RestartPolicy: "stale"}
			preflightCalled := false
			configureRestart(&c, "linux", 100, func() (unitRestartConfig, bool) {
				return unitRestartConfig{MainPID: tc.mainPID, Restart: tc.policy}, tc.known
			}, func(path string) error {
				preflightCalled = true
				if path != c.ExecPath {
					t.Fatal("wrong exec path")
				}
				return tc.preflightErr
			})
			child := tc.known && tc.mainPID > 0 && tc.mainPID != 100
			if preflightCalled != child {
				t.Fatalf("preflight=%v, child=%v", preflightCalled, child)
			}
			if c.RestartMethod != tc.method || c.RestartBlockReason != tc.reason || c.CanRestart != (tc.method != RestartNone) {
				t.Fatalf("unexpected capability: %+v", c)
			}
			if child && (c.Supervisor != SupervisorNone || c.RestartPolicy != "") {
				t.Fatalf("leaked parent policy: %+v", c)
			}
		})
	}
}

func restartTestManager(path string) *Manager {
	m := NewManager()
	m.detect = func() Capability { return Capability{CanRestart: true, RestartMethod: RestartSystemd, ExecPath: path} }
	return m
}

func TestPrepareRestartExclusionAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xmall")
	m := restartTestManager(path)
	plan, err := m.PrepareRestart()
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cancel()
	if RestartRequested() {
		t.Fatal("preparation must not request shutdown before response")
	}
	if _, err := m.PrepareRestart(); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("duplicate restart: %v", err)
	}
	if err := m.acquire(); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("update exclusion: %v", err)
	}
	if err := m.Rollback(false); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("rollback exclusion: %v", err)
	}
	if _, err := acquireBinaryLock(path); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("cross-process exclusion: %v", err)
	}
	other := restartTestManager(filepath.Join(t.TempDir(), "other"))
	if _, err := other.PrepareRestart(); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("global duplicate restart: %v", err)
	}
	plan.Cancel()
	plan.Cancel()
	if m.Running() {
		t.Fatal("cancel retained manager reservation")
	}
	unlock, err := acquireBinaryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	second, err := m.PrepareRestart()
	if err != nil {
		t.Fatal(err)
	}
	second.Cancel()
}

func TestPrepareRestartFailuresReleaseReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xmall")
	m := restartTestManager(path)
	unlock, err := acquireBinaryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PrepareRestart(); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("busy lock: %v", err)
	}
	unlock()
	if m.Running() || restartReserved.Load() {
		t.Fatal("failed lock left reservation")
	}
	m.detect = func() Capability { return Capability{} }
	if _, err := m.PrepareRestart(); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("unsupported: %v", err)
	}
	m.detect = func() Capability { return Capability{CanRestart: true, RestartMethod: RestartExec, ExecPath: path} }
	if _, err := m.PrepareRestart(); err == nil {
		t.Fatal("missing target must fail before shutdown")
	}
	if m.Running() || restartReserved.Load() || RestartRequested() {
		t.Fatal("failed preflight retained reservation")
	}
}

func TestFinishRestartFailureDoesNotRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xmall")
	if err := os.WriteFile(path, []byte("invalid new image"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".backup", []byte("old image"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := restartTestManager(path)
	plan, err := m.PrepareRestart()
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cancel()
	plan.method = RestartExec
	pendingRestart.Store(plan) // simulate completed drain without signalling test process
	if err := FinishRestart(); err == nil {
		t.Fatal("invalid image accepted after shutdown")
	}
	if m.Running() || RestartRequested() || restartReserved.Load() {
		t.Fatal("failed exec retained lock")
	}
	data, _ := os.ReadFile(path)
	backup, _ := os.ReadFile(path + ".backup")
	if string(data) != "invalid new image" || string(backup) != "old image" {
		t.Fatal("failure automatically rolled back binary")
	}
	unlock, err := acquireBinaryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestRestartLockBlocksCLIChild(t *testing.T) {
	if path := os.Getenv("XMALL_RESTART_LOCK_CHILD"); path != "" {
		if err := rollbackAt(path, false); !errors.Is(err, ErrUpdateInProgress) {
			t.Fatalf("CLI child bypassed restart lock: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "xmall")
	m := restartTestManager(path)
	plan, err := m.PrepareRestart()
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestRestartLockBlocksCLIChild$")
	cmd.Env = append(os.Environ(), "XMALL_RESTART_LOCK_CHILD="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestExecutablePathCapturedAtStartup(t *testing.T) {
	path, err := ExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatal("launch path is not absolute")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if path != startupExecutable {
		t.Fatal("launch executable changed")
	}
}
