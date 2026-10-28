//go:build !linux

package selfupdate

func preflightExec(string) error                   { return ErrNotSupported }
func execProcess(string, []string, []string) error { return ErrNotSupported }
