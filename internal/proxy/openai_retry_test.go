package proxy_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

// testConfigGetter implements config.RuntimeConfigGetter for tests.
type testConfigGetter struct {
	cfg *config.Config
}

func (g *testConfigGetter) Get() *config.Config { return g.cfg }

// retryEnabledConfig returns a minimal Config with retry enabled.
func retryEnabledConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Routing.Retry = config.RetryConfig{
		Enabled:            true,
		SameKeyRetries:     1,
		SameKeyBackoffSec:  1,
		KeyRetries:         2,
		ProviderRetries:    1,
		ProviderBackoffSec: 1,
		MaxTotalAttempts:   5,
	}
	return cfg
}

// TestOpenAIRetry_FailoverHighToNormalPriority verifies that when the
// high-priority key receives a 429, the relay automatically falls through
// to the normal-priority key within the same request (three-level retry).
func TestOpenAIRetry_FailoverHighToNormalPriority(t *testing.T) {
	var requests atomic.Int32

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		auth := r.Header.Get("Authorization")
		if auth == "Bearer sk-high-priority-key" {
			// High-priority key always returns 429.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`)
			return
		}
		// Normal-priority key succeeds.
		assert.Equal(t, "Bearer sk-normal-priority-key", auth, "second attempt should use normal-priority key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"id":"chatcmpl-ok-%d","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`, n)
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-high-priority-key", Priority: 10},
			{APIKey: "sk-normal-priority-key", Priority: 1},
		},
	})
	require.NoError(t, err)

	prov := newOpenAITestProvider(t, openaiProvider, backend.URL, nil)
	infos := []router.ProviderInfo{proxy.TestProviderInfo(prov)}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderPools: func() map[string]*keypool.KeyPool {
			return map[string]*keypool.KeyPool{openaiProvider: pool}
		},
		ConfigProvider: &testConfigGetter{cfg: retryEnabledConfig()},
		DebugOptions:   proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"should succeed after falling through from high-priority 429 to normal-priority key")
	assert.GreaterOrEqual(t, requests.Load(), int32(2),
		"backend should have received at least 2 requests (high-priority 429 + normal-priority 200)")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["id"], "chatcmpl-ok-", "should return successful completion")
}

// TestOpenAIRetry_StreamingFailover verifies the same failover for streaming
// requests: high-priority 429 → normal-priority 200 with SSE streaming.
func TestOpenAIRetry_StreamingFailover(t *testing.T) {
	var requests atomic.Int32

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		auth := r.Header.Get("Authorization")
		if auth == "Bearer sk-high-priority-key" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-stream-%d\",\"model\":\"gpt-4\"}\n\ndata: [DONE]\n\n", n)
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-high-priority-key", Priority: 10},
			{APIKey: "sk-normal-priority-key", Priority: 1},
		},
	})
	require.NoError(t, err)

	prov := newOpenAITestProvider(t, openaiProvider, backend.URL, nil)
	infos := []router.ProviderInfo{proxy.TestProviderInfo(prov)}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderPools: func() map[string]*keypool.KeyPool {
			return map[string]*keypool.KeyPool{openaiProvider: pool}
		},
		ConfigProvider: &testConfigGetter{cfg: retryEnabledConfig()},
		DebugOptions:   proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "streaming retry should succeed with normal-priority key")
	assert.GreaterOrEqual(t, requests.Load(), int32(2), "backend should see both key attempts")
	assert.Contains(t, rec.Body.String(), "chatcmpl-stream-", "should stream the successful response")
}
