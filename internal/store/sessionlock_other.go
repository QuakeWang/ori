//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package store

import "os"

// Fallback for unsupported platforms. This preserves existing behavior but
// does not add cross-process serialization outside Unix-like systems.
func lockSessionFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

func unlockSessionFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
