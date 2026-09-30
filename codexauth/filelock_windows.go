//go:build windows

package codexauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// lockRefresh acquires an exclusive cross-process lock serializing token
// refreshes for one auth file. The OS releases the lock if the process dies.
func lockRefresh(authPath string, wait time.Duration) (func(), error) {
	lockPath := filepath.Join(os.TempDir(), "cc-relay-codex-auth-refresh.lock")
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(lockPath),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("create Codex auth refresh lock: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		overlapped := new(windows.Overlapped)
		err = windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
				_ = windows.CloseHandle(handle)
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("lock Codex auth refresh: %w", err)
		}
		if time.Now().After(deadline) {
			_ = windows.CloseHandle(handle)
			return nil, ErrRefreshBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}
