package codexauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAuthFile(t *testing.T, dir string, accessToken, accountID string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	payload := map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"id_token":      "idt",
			"access_token":  accessToken,
			"refresh_token": "rt",
			"account_id":    accountID,
		},
	}
	data, _ := json.Marshal(payload)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	return path
}

func TestTokenSourceGet(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthFile(t, dir, "at-1", "acct-1")

	src, err := NewTokenSource(path)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}

	tok, err := src.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tok.AccessToken != "at-1" || tok.AccountID != "acct-1" || tok.AuthMode != "chatgpt" {
		t.Errorf("token = %+v", tok)
	}

	// Rotation: rewrite the file, Get must observe the new token.
	writeAuthFile(t, dir, "at-2", "acct-2")
	// Nudge mtime past the cached timestamp: an immediate rewrite of the
	// same-size file can land within one filesystem timestamp tick.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("bump auth file mtime: %v", err)
	}
	tok, err = src.Get()
	if err != nil {
		t.Fatalf("Get after rotation: %v", err)
	}
	if tok.AccessToken != "at-2" {
		t.Errorf("access token = %q, want at-2", tok.AccessToken)
	}
}

func TestTokenSourceMissingFile(t *testing.T) {
	src, err := NewTokenSource(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	_, err = src.Get()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error = %v, want login guidance", err)
	}
}

func TestTokenSourceNoAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"a"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	src, _ := NewTokenSource(path)
	_, err := src.Get()
	if err == nil || !strings.Contains(err.Error(), "no access token") {
		t.Errorf("error = %v, want no-access-token guidance", err)
	}
}

func TestTokenSourceInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{bad`), 0600); err != nil {
		t.Fatal(err)
	}
	src, _ := NewTokenSource(path)
	if _, err := src.Get(); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
