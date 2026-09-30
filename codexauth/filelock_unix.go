//go:build unix

package codexauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// lockRefresh acquires an exclusive cross-process lock serializing token
// refreshes for one auth file. The OS releases the lock if the process dies.
func lockRefresh(authPath string, wait time.Duration) (func(), error) {
	lockPath := filepath.Join(os.TempDir(), "cc-relay-codex-auth-refresh.lock")
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create Codex auth refresh lock: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = unix.Close(fd)
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("lock Codex auth refresh: %w", err)
		}
		if time.Now().After(deadline) {
			_ = unix.Close(fd)
			return nil, ErrRefreshBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}
