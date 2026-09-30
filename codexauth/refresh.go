package codexauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OAuth constants matching the Codex client (codex-rs login/src/auth/manager.rs).
const (
	// DefaultRefreshTokenURL is the ChatGPT OAuth token endpoint used by the
	// Codex client for refresh_token grants.
	DefaultRefreshTokenURL = "https://auth.openai.com/oauth/token"
	// DefaultClientID is the public Codex CLI OAuth client ID.
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// accessRefreshWindow mirrors codex-rs
	// CHATGPT_ACCESS_TOKEN_REFRESH_WINDOW_MINUTES: refresh proactively when
	// the access token expires within this window.
	accessRefreshWindow = 5 * time.Minute
	// maxRefreshAge mirrors codex-rs TOKEN_REFRESH_INTERVAL: refresh when
	// last_refresh is older than this and the token expiry is unknown.
	maxRefreshAge = 8 * 24 * time.Hour

	// refreshLockWait bounds how long Refresh waits for a concurrent
	// refresher (another process holding the cross-process lock).
	refreshLockWait = 10 * time.Second
)

// ErrRefreshTokenRejected reports that the stored refresh token was rejected
// (expired, reused, or revoked). The only recovery is signing in again with
// the Codex app or `codex login`.
var ErrRefreshTokenRejected = errors.New("Codex refresh token was rejected — sign in again with the Codex app or run `codex login`")

// ErrRefreshBusy reports that another process is currently refreshing the
// same auth file and did not finish within refreshLockWait.
var ErrRefreshBusy = errors.New("another Codex token refresh is in progress")

// refreshResponse mirrors the optional fields returned by the token
// endpoint. Every field may be absent; codex-rs only overwrites the fields
// that are present.
type refreshResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// EnsureFresh returns a usable access token, refreshing it first only when
// it is missing, expired, expiring within accessRefreshWindow, or stale per
// last_refresh. The refresh policy mirrors the Codex client.
func (s *TokenSource) EnsureFresh(ctx context.Context) (*Token, error) {
	return s.refreshLocked(ctx, false)
}

// Refresh forces a token refresh using the stored refresh_token and
// persists the rotated tokens back to the auth file.
func (s *TokenSource) Refresh(ctx context.Context) (*Token, error) {
	return s.refreshLocked(ctx, true)
}

func (s *TokenSource) refreshLocked(ctx context.Context, force bool) (*Token, error) {
	// Serialize refreshes across processes touching the same auth file.
	unlock, err := lockRefresh(s.path, refreshLockWait)
	if err != nil {
		return nil, err
	}
	defer unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-read after acquiring the lock: another process (including the
	// Codex app) may have refreshed while we waited.
	af, raw, err := readAuthFile(s.path)
	if err != nil {
		return nil, err
	}
	if !force && !needsRefresh(af) {
		return s.cacheLocked(af)
	}
	if af.Tokens.RefreshToken == "" {
		return nil, fmt.Errorf("Codex auth file %s has no refresh token — sign in again with the Codex app", s.path)
	}

	resp, err := s.requestRefresh(ctx, af.Tokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	if resp.AccessToken == "" && resp.IDToken == "" && resp.RefreshToken == "" {
		return nil, errors.New("Codex token refresh returned no tokens")
	}

	if err := persistRefreshedTokens(s.path, raw, resp); err != nil {
		return nil, err
	}

	// Build the refreshed token from the freshly parsed state so fields the
	// endpoint did not rotate (e.g. account_id) survive.
	refreshed := *af
	if resp.IDToken != "" {
		refreshed.Tokens.IDToken = resp.IDToken
	}
	if resp.AccessToken != "" {
		refreshed.Tokens.AccessToken = resp.AccessToken
	} else if refreshed.Tokens.AccessToken == "" {
		return nil, errors.New("Codex token refresh returned no access token")
	}
	if resp.RefreshToken != "" {
		refreshed.Tokens.RefreshToken = resp.RefreshToken
	}
	return s.cacheLocked(&refreshed)
}

// requestRefresh performs the same POST the Codex client performs: a JSON
// body of client_id, grant_type, and refresh_token.
func (s *TokenSource) requestRefresh(ctx context.Context, refreshToken string) (*refreshResponse, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     s.clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Codex token refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Codex token refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if isRefreshRejected(resp.StatusCode, respBody) {
			return nil, fmt.Errorf("%w (HTTP %d)", ErrRefreshTokenRejected, resp.StatusCode)
		}
		return nil, fmt.Errorf("Codex token refresh endpoint returned HTTP %d: %s", resp.StatusCode, summarizeBody(respBody))
	}

	var out refreshResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("Codex token refresh response was not valid JSON: %w", err)
	}
	return &out, nil
}

// needsRefresh mirrors the Codex client policy: refresh when the access
// token is missing or expires within accessRefreshWindow; when the expiry
// cannot be determined, fall back to the last_refresh age.
func needsRefresh(af *authFile) bool {
	if af.Tokens.AccessToken == "" {
		return af.Tokens.RefreshToken != ""
	}
	if exp, ok := jwtExpiry(af.Tokens.AccessToken); ok {
		return !exp.After(time.Now().Add(accessRefreshWindow))
	}
	if af.LastRefresh.IsZero() {
		return false
	}
	return time.Since(af.LastRefresh) > maxRefreshAge
}

// jwtExpiry decodes the exp claim of a JWT without verifying its signature.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// persistRefreshedTokens rewrites the auth file with the rotated tokens
// while preserving unknown fields, then replaces the original atomically.
func persistRefreshedTokens(path string, original []byte, resp *refreshResponse) error {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		return fmt.Errorf("Codex auth file %s contains invalid JSON: %w", path, err)
	}
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	if resp.IDToken != "" {
		tokens["id_token"] = resp.IDToken
	}
	if resp.AccessToken != "" {
		tokens["access_token"] = resp.AccessToken
	}
	if resp.RefreshToken != "" {
		tokens["refresh_token"] = resp.RefreshToken
	}
	doc["tokens"] = tokens
	doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeFileAtomic(path, out, 0o600)
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it over path, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth-refresh-*")
	if err != nil {
		return fmt.Errorf("stage Codex auth update: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("set Codex auth permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write Codex auth update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged Codex auth update: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace Codex auth file: %w", err)
	}
	return nil
}

// cacheLocked stores the token snapshot and mtime cache. Callers must hold
// s.mu (the stat happens after the atomic rename so the cache tracks the
// new file identity).
func (s *TokenSource) cacheLocked(af *authFile) (*Token, error) {
	if af.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("Codex auth file %s has no access token — re-login with the Codex app", s.path)
	}
	tok := &Token{
		AccessToken: af.Tokens.AccessToken,
		AccountID:   af.Tokens.AccountID,
		AuthMode:    af.AuthMode,
	}
	stat, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("cannot access Codex auth file %s: %w", s.path, err)
	}
	s.cached = tok
	s.mtime = stat.ModTime()
	s.size = stat.Size()
	return tok, nil
}

// isRefreshRejected maps endpoint failures that require a new login.
func isRefreshRejected(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"invalid_grant",
		"refresh_token_expired",
		"refresh_token_reused",
		"refresh_token_invalidated",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func summarizeBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
