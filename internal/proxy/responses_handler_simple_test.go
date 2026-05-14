package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/router"
)

type testResponsesRouter struct {
	selected router.ProviderInfo
	err      error
}

func (t *testResponsesRouter) Select(_ context.Context, _ []router.ProviderInfo) (router.ProviderInfo, error) {
	return t.selected, t.err
}

func (t *testResponsesRouter) Name() string { return "test" }

func TestResponsesHandler_MethodNotAllowed(t *testing.T) {
	handler, err := NewResponsesHandler(&ResponsesHandlerOptions{
		Router:    &testResponsesRouter{},
		Providers: func() []router.ProviderInfo { return nil },
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestResponsesHandler_InvalidJSON(t *testing.T) {
	handler, err := NewResponsesHandler(&ResponsesHandlerOptions{
		Router:    &testResponsesRouter{},
		Providers: func() []router.ProviderInfo { return nil },
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestResponsesHandler_MissingModel(t *testing.T) {
	handler, err := NewResponsesHandler(&ResponsesHandlerOptions{
		Router:    &testResponsesRouter{},
		Providers: func() []router.ProviderInfo { return nil },
	})
	require.NoError(t, err)

	body := map[string]any{
		"input": []map[string]any{{"type": "message", "message": map[string]any{"role": "user", "content": "hi"}}},
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestResponsesHandler_NoProviders(t *testing.T) {
	handler, err := NewResponsesHandler(&ResponsesHandlerOptions{
		Router:    &testResponsesRouter{},
		Providers: func() []router.ProviderInfo { return nil },
	})
	require.NoError(t, err)

	body := map[string]any{
		"model": "gpt-4",
		"input": []map[string]any{{"type": "message", "message": map[string]any{"role": "user", "content": "hi"}}},
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "server_error", errObj["type"])
}
