//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package store

import (
	"fmt"
	"os"
	"syscall"
)

func lockSessionFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("store: flock %s: %w", path, err)
	}
	return file, nil
}

func unlockSessionFile(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		_ = file.Close()
		return fmt.Errorf("store: unlock session file: %w", err)
	}
	return file.Close()
}
