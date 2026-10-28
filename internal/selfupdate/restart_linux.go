//go:build linux

package selfupdate

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func executableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("not a regular executable: %s", path)
	}
	if err := unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return err
	}
	if fs.Flags&unix.ST_NOEXEC != 0 {
		return fmt.Errorf("executable filesystem is mounted noexec")
	}
	return nil
}

func preflightExec(path string) error {
	if launchCWDErr != nil {
		return launchCWDErr
	}
	if launchCWDStatErr != nil {
		return launchCWDStatErr
	}
	cwd, err := os.Stat(launchCWD)
	if err != nil {
		return err
	}
	if !cwd.IsDir() || !os.SameFile(cwd, launchCWDInfo) {
		return fmt.Errorf("launch working directory has been replaced")
	}
	if err := unix.Faccessat(unix.AT_FDCWD, launchCWD, unix.X_OK, unix.AT_EACCESS); err != nil {
		return err
	}
	if len(launchArgs) == 0 {
		return fmt.Errorf("missing launch arguments")
	}
	// Every Linux kernel supports at least 128 KiB for argv+env. Stay below
	// that floor (including pointer overhead) rather than discovering E2BIG
	// only after the old HTTP service has been drained.
	values := append(append([]string(nil), launchArgs...), launchEnv...)
	argumentBytes := (len(values) + 2) * 8
	for _, value := range values {
		argumentBytes += len(value) + 1
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid launch arguments/environment")
		}
	}
	if argumentBytes > 120*1024 {
		return fmt.Errorf("launch arguments/environment exceed safe exec budget")
	}
	if _, err := os.ReadDir("/proc/self/fd"); err != nil {
		return fmt.Errorf("cannot inspect inherited descriptors: %w", err)
	}
	if err := executableFile(path); err != nil {
		return err
	}
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("target is not a native ELF executable: %w", err)
	}
	defer f.Close()
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64, "386": elf.EM_386, "arm": elf.EM_ARM, "riscv64": elf.EM_RISCV, "ppc64": elf.EM_PPC64, "ppc64le": elf.EM_PPC64, "s390x": elf.EM_S390, "loong64": elf.EM_LOONGARCH}
	if machine, ok := machines[runtime.GOARCH]; !ok || f.Machine != machine {
		return fmt.Errorf("ELF architecture does not match %s", runtime.GOARCH)
	}
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("ELF is not executable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	entryFound := false
	for _, p := range f.Progs {
		if p.Off > uint64(info.Size()) || p.Filesz > uint64(info.Size())-p.Off {
			return fmt.Errorf("truncated ELF segment")
		}
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 && f.Entry >= p.Vaddr && f.Entry-p.Vaddr < p.Memsz {
			entryFound = true
		}
		if p.Type == elf.PT_INTERP {
			if p.Filesz > 4096 {
				return fmt.Errorf("invalid ELF interpreter")
			}
			data, err := io.ReadAll(p.Open())
			if err != nil {
				return err
			}
			interpreter := strings.TrimRight(string(data), "\x00")
			if !strings.HasPrefix(interpreter, "/") {
				return fmt.Errorf("invalid ELF interpreter path")
			}
			if err := executableFile(interpreter); err != nil {
				return fmt.Errorf("ELF interpreter unavailable: %w", err)
			}
		}
	}
	if !entryFound {
		return fmt.Errorf("ELF entry point is not executable")
	}
	return nil
}

func execProcess(path string, args, env []string) error {
	// Go-created files/sockets (including the held flock) are O_CLOEXEC.
	// Also seal any descriptors inherited from the hosting panel. Do not close
	// them before exec: a failed syscall must still allow diagnostics/cleanup.
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("enumerate inherited descriptors: %w", err)
	}
	for _, fd := range fds {
		var n int
		if _, err := fmt.Sscanf(fd.Name(), "%d", &n); err == nil && n > 2 {
			if _, err := unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && err != unix.EBADF {
				return fmt.Errorf("seal inherited descriptor %d: %w", n, err)
			}
		}
	}
	return syscall.Exec(path, args, env)
}
