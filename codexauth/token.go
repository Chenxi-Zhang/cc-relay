// Package codexauth provides access to the Codex CLI login state stored in
// ~/.codex/auth.json. Reading is cached per file mtime so rotation by the
// Codex app is picked up automatically. The package can also refresh the
// OAuth tokens itself using the same endpoint and client ID as the Codex
// client (see Refresh and EnsureFresh), which keeps the local login usable
// without opening the Codex app.
package codexauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// authFile mirrors the subset of ~/.codex/auth.json the gateway needs.
type authFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh time.Time `json:"last_refresh"`
}

// Token is a snapshot of the Codex login state.
type Token struct {
	AccessToken string
	AccountID   string
	AuthMode    string
}

// Options configures optional behavior of a TokenSource.
type Options struct {
	// HTTPClient used for refresh requests. Nil uses a default client with
	// a 30 second timeout and environment-proxy-aware transport.
	HTTPClient *http.Client
	// TokenURL overrides the OAuth token endpoint (tests only).
	TokenURL string
	// ClientID overrides the OAuth client ID (tests only).
	ClientID string
}

// TokenSource reads and caches the Codex CLI auth file. It can also refresh
// the stored OAuth tokens. It is safe for concurrent use.
type TokenSource struct {
	path     string
	client   *http.Client
	tokenURL string
	clientID string

	mu     sync.Mutex
	cached *Token
	mtime  time.Time
	size   int64
}

// DefaultAuthFilePath returns the default Codex auth file location.
func DefaultAuthFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate user home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

// NewTokenSource creates a token source for the given auth file path.
// An empty path falls back to DefaultAuthFilePath.
func NewTokenSource(path string) (*TokenSource, error) {
	return NewTokenSourceWithOptions(path, Options{})
}

// NewTokenSourceWithOptions creates a token source with custom options.
// An empty path falls back to DefaultAuthFilePath.
func NewTokenSourceWithOptions(path string, opts Options) (*TokenSource, error) {
	if path == "" {
		var err error
		path, err = DefaultAuthFilePath()
		if err != nil {
			return nil, err
		}
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	tokenURL := opts.TokenURL
	if tokenURL == "" {
		tokenURL = DefaultRefreshTokenURL
	}
	clientID := opts.ClientID
	if clientID == "" {
		clientID = DefaultClientID
	}
	return &TokenSource{
		path:     path,
		client:   client,
		tokenURL: tokenURL,
		clientID: clientID,
	}, nil
}

// Path returns the auth file path being watched.
func (s *TokenSource) Path() string {
	return s.path
}

// Get returns the current token, re-reading the file when it changed
// since the last read. Returns an error when Codex is not logged in.
func (s *TokenSource) Get() (*Token, error) {
	stat, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Codex is not logged in: %s not found — open the Codex app or run `codex login` once", s.path)
		}
		return nil, fmt.Errorf("cannot access Codex auth file %s: %w", s.path, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != nil && stat.ModTime().Equal(s.mtime) && stat.Size() == s.size {
		return s.cached, nil
	}

	af, _, err := readAuthFile(s.path)
	if err != nil {
		return nil, err
	}
	if af.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("Codex auth file %s has no access token — re-login with the Codex app", s.path)
	}

	tok := &Token{
		AccessToken: af.Tokens.AccessToken,
		AccountID:   af.Tokens.AccountID,
		AuthMode:    af.AuthMode,
	}
	s.cached = tok
	s.mtime = stat.ModTime()
	s.size = stat.Size()

	return tok, nil
}

// readAuthFile loads and parses the auth file, returning both the parsed
// subset and the raw bytes (needed to preserve unknown fields on write).
func readAuthFile(path string) (*authFile, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("Codex is not logged in: %s not found — open the Codex app or run `codex login` once", path)
		}
		return nil, nil, fmt.Errorf("cannot read Codex auth file %s: %w", path, err)
	}
	var af authFile
	if err := json.Unmarshal(data, &af); err != nil {
		return nil, nil, fmt.Errorf("Codex auth file %s contains invalid JSON: %w", path, err)
	}
	return &af, data, nil
}
