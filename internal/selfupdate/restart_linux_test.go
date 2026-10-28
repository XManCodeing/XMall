//go:build linux

package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Only copies of the test executable are replaced. No service/config/database
// is started. The replacement image checks PID, launch context and flock reuse.
func TestExecReplacementSubprocess(t *testing.T) {
	if stage := os.Getenv("XMALL_EXEC_TEST_STAGE"); stage != "" {
		path := os.Getenv("XMALL_EXEC_TEST_PATH")
		if stage == "new" {
			if strconv.Itoa(os.Getpid()) != os.Getenv("XMALL_EXEC_TEST_PID") {
				t.Fatal("exec changed PID")
			}
			cwd, _ := os.Getwd()
			if cwd != os.Getenv("XMALL_EXEC_TEST_CWD") {
				t.Fatalf("cwd lost: %s", cwd)
			}
			if os.Args[0] != "preserved-argv0" {
				t.Fatal("argv not preserved")
			}
			unlock, err := acquireBinaryLock(path)
			if err != nil {
				t.Fatalf("lock inherited across exec: %v", err)
			}
			unlock()
			fd, _ := strconv.Atoi(os.Getenv("XMALL_EXEC_TEST_FD"))
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
				t.Fatalf("inherited descriptor survived exec: %v", err)
			}
			fmt.Println("EXEC_REPLACEMENT_OK")
			return
		}
		if err := os.Rename(path, path+".backup"); err != nil {
			t.Fatal(err)
		}
		copyExecutable(t, path+".backup", path)
		resolved, err := ExecutablePath()
		if err != nil || resolved != path {
			t.Fatalf("resolved backup instead of original target: %q %v", resolved, err)
		}
		if err := preflightExec(path); err != nil {
			t.Fatal(err)
		}
		m := NewManager()
		m.detect = func() Capability { return Capability{CanRestart: true, RestartMethod: RestartExec, ExecPath: path} }
		plan, err := m.PrepareRestart()
		if err != nil {
			t.Fatal(err)
		}
		defer plan.Cancel()
		inherited, err := os.Open("/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		defer inherited.Close()
		// A high descriptor avoids confusing a new runtime FD with the old one.
		fd, err := unix.FcntlInt(inherited.Fd(), unix.F_DUPFD, 200)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		env := append(os.Environ(), "XMALL_EXEC_TEST_STAGE=new", "XMALL_EXEC_TEST_PID="+strconv.Itoa(os.Getpid()), "XMALL_EXEC_TEST_FD="+strconv.Itoa(fd))
		// os/exec deduplicates env but syscall.Exec doesn't; remove old stage.
		filtered := []string{}
		for _, item := range env {
			if item != "XMALL_EXEC_TEST_STAGE=old" {
				filtered = append(filtered, item)
			}
		}
		launchArgs = []string{"preserved-argv0", "-test.run=^TestExecReplacementSubprocess$", "-test.v"}
		launchEnv = filtered
		if err := os.Chdir("/"); err != nil {
			t.Fatal(err)
		}
		pendingRestart.Store(plan) // simulate completed graceful shutdown
		if err := FinishRestart(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("successful exec returned")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "xmall")
	exe, _ := os.Executable()
	copyExecutable(t, exe, path)
	cmd := exec.Command(path, "-test.run=^TestExecReplacementSubprocess$", "-test.v")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XMALL_EXEC_TEST_STAGE=old", "XMALL_EXEC_TEST_PATH="+path, "XMALL_EXEC_TEST_CWD="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "EXEC_REPLACEMENT_OK") {
		t.Fatalf("exec child: %v\n%s", err, out)
	}
}

func copyExecutable(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestExecPreflightAndFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad")
	for _, data := range []string{"", "#!/bin/sh\nexit 0\n", "not an ELF"} {
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := preflightExec(path); err == nil {
			t.Fatal("invalid ELF accepted")
		}
	}
	if err := preflightExec(dir); err == nil {
		t.Fatal("directory accepted")
	}
	// syscall failure is exercised in a child so sealing inherited descriptors
	// cannot interfere with the parent go test process.
	if os.Getenv("XMALL_EXEC_FAILURE_CHILD") == "1" {
		if err := execProcess(filepath.Join(dir, "missing"), []string{"xmall"}, os.Environ()); err == nil {
			t.Fatal("missing target succeeded")
		}
		return
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestExecPreflightAndFailure$")
	cmd.Env = append(os.Environ(), "XMALL_EXEC_FAILURE_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failure child: %v %s", err, out)
	}
}
