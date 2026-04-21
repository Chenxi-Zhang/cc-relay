package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/proxy"
)

// TestStreamingRetry_429Then200_SameKey verifies that a streaming request receiving a 429
// on the first attempt retries with the same key and succeeds.
func TestStreamingRetry_429Then200_SameKey(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	callCount := 0
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		count := callCount
		mu.Unlock()

		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: true, SameKeyRetries: 1}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: message_start")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, callCount, "expected 2 backend calls (429 + retry)")
}

// TestStreamingRetry_200Direct_NoRetry verifies that a successful streaming response
// on the first attempt goes through directly with no buffering.
func TestStreamingRetry_200Direct_NoRetry(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	callCount := 0
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: true, SameKeyRetries: 1}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: message_start")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callCount, "successful streaming should not trigger retry")
}

// TestStreamingRetry_All429_ReturnsLast429 verifies that when all retry attempts return 429,
// the last 429 response is returned to the client.
func TestStreamingRetry_All429_ReturnsLast429(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	callCount := 0
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: true, SameKeyRetries: 1}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "rate_limit_error")
}

// TestStreamingRetry_Non429Error_ReturnedDirectly verifies that a non-429 error
// (e.g., 500) is returned directly without retry.
func TestStreamingRetry_Non429Error_ReturnedDirectly(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	callCount := 0
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"internal error"}}`))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: true, SameKeyRetries: 1}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "api_error")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callCount, "non-429 error should not trigger retry")
}

// TestStreamingRetry_BodyPreservedAcrossAttempts verifies that the request body
// is preserved across streaming retry attempts.
func TestStreamingRetry_BodyPreservedAcrossAttempts(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	var mu sync.Mutex
	var bodies []string
	callCount := 0

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("backend failed to read body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		mu.Lock()
		bodies = append(bodies, string(body))
		callCount++
		count := callCount
		mu.Unlock()

		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: true, SameKeyRetries: 1}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "expected 2 backend calls (initial + retry)")
	assert.Equal(t, requestBody, bodies[0], "first call should have full body")
	assert.Equal(t, requestBody, bodies[1], "retry call should have full body (not empty)")
}

// TestStreamingRetry_Disabled_Returns429Directly verifies that when retry is disabled,
// streaming requests return 429 immediately without retry.
func TestStreamingRetry_Disabled_Returns429Directly(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`

	callCount := 0
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`))
	}))
	t.Cleanup(backend.Close)

	provider := proxy.NewTestProvider(backend.URL)
	handler, err := proxy.NewHandler(&proxy.HandlerOptions{
		Provider:       provider,
		APIKey:         testKey,
		RoutingConfig:  &config.RoutingConfig{Retry: config.RetryConfig{Enabled: false}},
		DebugOptions:   proxy.TestDebugOptions(),
		ProviderInfos:  nil,
		ProviderRouter: nil,
		ProviderPools:  nil,
	})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", testKey)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callCount, "disabled retry should only call backend once")
}
