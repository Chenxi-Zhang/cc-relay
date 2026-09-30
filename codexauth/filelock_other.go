//go:build !windows && !unix

package codexauth

import "time"

// lockRefresh is a no-op fallback for platforms without a portable
// exclusive file lock. Correctness still holds because refresh tokens are
// single-use server-side: a racing refresh fails visibly instead of
// corrupting the stored state.
func lockRefresh(authPath string, wait time.Duration) (func(), error) {
	return func() {}, nil
}
