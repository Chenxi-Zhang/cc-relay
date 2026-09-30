package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeJWT builds an unsigned JWT whose only claim is exp.
func makeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{"exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func writeRefreshableAuth(t *testing.T, dir, accessToken, refreshToken string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	payload := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": "preserve-me",
		"tokens": map[string]string{
			"id_token":      "old-id",
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"account_id":    "acct-1",
		},
		"last_refresh": time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano),
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newRefreshBackend(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshRotatesTokensAndPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeRefreshableAuth(t, dir, "old-access", "old-refresh")

	var gotBody map[string]string
	srv := newRefreshBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id_token":      "new-id",
			"access_token":  makeJWT(t, time.Now().Add(24*time.Hour)),
			"refresh_token": "new-refresh",
		})
	})

	src, err := NewTokenSourceWithOptions(path, Options{TokenURL: srv.URL, ClientID: "test-client"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken == "old-access" {
		t.Fatal("access token was not rotated")
	}
	if tok.AccountID != "acct-1" || tok.AuthMode != "chatgpt" {
		t.Errorf("token = %+v", tok)
	}

	// The request must match the Codex client wire format.
	if gotBody["grant_type"] != "refresh_token" || gotBody["client_id"] != "test-client" || gotBody["refresh_token"] != "old-refresh" {
		t.Errorf("refresh body = %+v", gotBody)
	}

	// The persisted file must rotate tokens and preserve unknown fields.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["OPENAI_API_KEY"] != "preserve-me" {
		t.Errorf("unknown field lost: %v", doc["OPENAI_API_KEY"])
	}
	tokens := doc["tokens"].(map[string]any)
	if tokens["refresh_token"] != "new-refresh" || tokens["id_token"] != "new-id" {
		t.Errorf("tokens = %+v", tokens)
	}
	if last, _ := tokens["last_refresh"].(string); last != "" {
		t.Errorf("last_refresh leaked into tokens: %q", last)
	}

	// Get() must serve the refreshed token from the updated file.
	tok, err = src.Get()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != tokens["access_token"] {
		t.Errorf("Get after refresh = %q, want %v", tok.AccessToken, tokens["access_token"])
	}
}

func TestEnsureFreshSkipsValidToken(t *testing.T) {
	dir := t.TempDir()
	path := writeRefreshableAuth(t, dir, makeJWT(t, time.Now().Add(24*time.Hour)), "rt")

	called := false
	srv := newRefreshBackend(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	src, _ := NewTokenSourceWithOptions(path, Options{TokenURL: srv.URL})
	tok, err := src.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if called {
		t.Error("refresh endpoint was called for a valid token")
	}
	if tok.AccessToken == "" {
		t.Error("no access token returned")
	}
}

func TestEnsureFreshRefreshesExpiringToken(t *testing.T) {
	dir := t.TempDir()
	path := writeRefreshableAuth(t, dir, makeJWT(t, time.Now().Add(time.Minute)), "rt")

	srv := newRefreshBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": makeJWT(t, time.Now().Add(24*time.Hour)),
		})
	})
	src, _ := NewTokenSourceWithOptions(path, Options{TokenURL: srv.URL})
	tok, err := src.EnsureFresh(context.Background())
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if exp, ok := jwtExpiry(tok.AccessToken); !ok || time.Until(exp) < 23*time.Hour {
		t.Errorf("token was not refreshed: %v", tok.AccessToken)
	}
}

func TestRefreshRejectionLeavesAuthFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := writeRefreshableAuth(t, dir, makeJWT(t, time.Now().Add(time.Minute)), "rt")
	before, _ := os.ReadFile(path)

	srv := newRefreshBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh_token_reused"}`))
	})
	src, _ := NewTokenSourceWithOptions(path, Options{TokenURL: srv.URL})
	_, err := src.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if !strings.Contains(err.Error(), ErrRefreshTokenRejected.Error()) {
		t.Errorf("error = %v, want re-login guidance", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("auth file changed despite a rejected refresh")
	}
}
