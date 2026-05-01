package proxy_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

const openAICompletionResponse = `{"id":"%s","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_FailoverBetweenProviders
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_FailoverBetweenProviders(t *testing.T) {
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-backend-b")))
	}))
	defer backendB.Close()

	provA := newOpenAITestProvider(t, "provider-a", backendA.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backendB.URL, nil)

	infos := []router.ProviderInfo{
		proxy.TestProviderInfoWithHealth(provA, func() bool { return false }),
		proxy.TestProviderInfoWithHealth(provB, func() bool { return true }),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"provider-a": "sk-key-a",
				"provider-b": "sk-key-b",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "should succeed via healthy provider-b")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-backend-b", resp["id"], "response should come from backend B")
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_KeyPoolCycling
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_KeyPoolCycling(t *testing.T) {
	var mu sync.Mutex
	seenKeys := make(map[string]int)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seenKeys[auth]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-keypool")))
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "round_robin",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-pool-alpha"},
			{APIKey: "sk-pool-beta"},
			{APIKey: "sk-pool-gamma"},
		},
	})
	require.NoError(t, err)

	handler := newOpenAIHandlerWithPool(t, backend.URL, pool)

	for i := 0; i < 3; i++ {
		req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should succeed", i+1)

		assert.NotEmpty(t, rec.Header().Get("X-CC-Relay-Key-ID"), "request %d should have key ID header", i+1)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, len(seenKeys), "all 3 pool keys should have been used exactly once")

	for _, auth := range []string{
		"Bearer sk-pool-alpha",
		"Bearer sk-pool-beta",
		"Bearer sk-pool-gamma",
	} {
		assert.Equal(t, 1, seenKeys[auth], "key %s should be used exactly once", auth)
	}
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_AllProvidersDown
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_AllProvidersDown(t *testing.T) {
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"internal_error"}}`))
	}))
	defer backendB.Close()

	provA := newOpenAITestProvider(t, "provider-a", backendA.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backendB.URL, nil)

	infos := []router.ProviderInfo{
		proxy.TestProviderInfo(provA),
		proxy.TestProviderInfo(provB),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"provider-a": "sk-key-a",
				"provider-b": "sk-key-b",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, "should proxy the 500 from backend")

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "internal server error", errResp.Error.Message)
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_AllProvidersUnhealthyReturns503
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_AllProvidersUnhealthyReturns503(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-ok")))
	}))
	defer backend.Close()

	provA := newOpenAITestProvider(t, "provider-a", backend.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backend.URL, nil)

	// Both providers marked unhealthy.
	infos := []router.ProviderInfo{
		proxy.TestProviderInfoWithHealth(provA, func() bool { return false }),
		proxy.TestProviderInfoWithHealth(provB, func() bool { return false }),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"provider-a": "sk-key-a",
				"provider-b": "sk-key-b",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Contains(t, errResp.Error.Message, "failed to select provider")
	assert.Equal(t, "server_error", errResp.Error.Type)
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_SingleProviderRecovery
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_SingleProviderRecovery(t *testing.T) {
	var requestCount atomic.Int32

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"temporary failure","type":"server_error","code":"internal_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-recovered")))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "first request should fail with 500")

	req = newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "second request should fail with 500")

	req = newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "third request should succeed after recovery")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-recovered", resp["id"])
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_KeysExhaustedReturns429
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_KeysExhaustedReturns429(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-ok")))
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-limited-key", RPMLimit: 1},
		},
	})
	require.NoError(t, err)

	handler := newOpenAIHandlerWithPool(t, backend.URL, pool)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "first request within RPM should succeed")

	got429 := false
	for i := 0; i < 10; i++ {
		req = newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	assert.True(t, got429, "should eventually get 429 when key is exhausted")
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_FailoverPriorityOrder
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_FailoverPriorityOrder(t *testing.T) {
	var mu sync.Mutex
	var selectedBackend string

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		selectedBackend = "A"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-a")))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		selectedBackend = "B"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-b")))
	}))
	defer backendB.Close()

	backendC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		selectedBackend = "C"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-c")))
	}))
	defer backendC.Close()

	provA := newOpenAITestProvider(t, "provider-a", backendA.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backendB.URL, nil)
	provC := newOpenAITestProvider(t, "provider-c", backendC.URL, nil)

	infos := []router.ProviderInfo{
		{
			Provider:  provA,
			IsHealthy: func() bool { return true },
			Weight:    0,
			Priority:  0,
		},
		{
			Provider:  provB,
			IsHealthy: func() bool { return true },
			Weight:    0,
			Priority:  5,
		},
		{
			Provider:  provC,
			IsHealthy: func() bool { return true },
			Weight:    0,
			Priority:  10,
		},
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"provider-a": "sk-key-a",
				"provider-b": "sk-key-b",
				"provider-c": "sk-key-c",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	mu.Lock()
	assert.Equal(t, "C", selectedBackend, "failover router should select highest priority provider")
	mu.Unlock()
	assert.Equal(t, "chatcmpl-c", resp["id"])
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_FailoverToNextWhenPrimaryUnhealthy
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_FailoverToNextWhenPrimaryUnhealthy(t *testing.T) {
	var mu sync.Mutex
	var hitBackends []string

	markHit := func(name string) {
		mu.Lock()
		hitBackends = append(hitBackends, name)
		mu.Unlock()
	}

	backendPrimary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markHit("primary")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backendPrimary.Close()

	backendSecondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markHit("secondary")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-secondary")))
	}))
	defer backendSecondary.Close()

	provPrimary := newOpenAITestProvider(t, "primary", backendPrimary.URL, nil)
	provSecondary := newOpenAITestProvider(t, "secondary", backendSecondary.URL, nil)

	infos := []router.ProviderInfo{
		{
			Provider:  provPrimary,
			IsHealthy: func() bool { return false },
			Weight:    0,
			Priority:  10,
		},
		{
			Provider:  provSecondary,
			IsHealthy: func() bool { return true },
			Weight:    0,
			Priority:  5,
		},
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"primary":   "sk-key-primary",
				"secondary": "sk-key-secondary",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-secondary", resp["id"])

	mu.Lock()
	assert.NotContains(t, hitBackends, "primary", "unhealthy primary should not receive requests")
	assert.Contains(t, hitBackends, "secondary", "healthy secondary should receive the request")
	mu.Unlock()
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_KeyPoolLeastLoadedDistribution
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_KeyPoolLeastLoadedDistribution(t *testing.T) {
	var mu sync.Mutex
	keyUsage := make(map[string]int)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		keyUsage[auth]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-distribution")))
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "round_robin",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-ll-1"},
			{APIKey: "sk-ll-2"},
			{APIKey: "sk-ll-3"},
		},
	})
	require.NoError(t, err)

	handler := newOpenAIHandlerWithPool(t, backend.URL, pool)

	const numRequests = 6
	for i := 0; i < numRequests; i++ {
		req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should succeed", i+1)
	}

	mu.Lock()
	defer mu.Unlock()

	usedKeys := 0
	for _, auth := range []string{
		"Bearer sk-ll-1",
		"Bearer sk-ll-2",
		"Bearer sk-ll-3",
	} {
		if keyUsage[auth] > 0 {
			usedKeys++
		}
	}
	assert.Equal(t, 3, usedKeys, "all 3 keys should be used with least_loaded strategy")
	assert.Equal(t, numRequests, keyUsage["Bearer sk-ll-1"]+keyUsage["Bearer sk-ll-2"]+keyUsage["Bearer sk-ll-3"],
		"total requests should equal %d", numRequests)
}

// ---------------------------------------------------------------------------
// TestOpenAIIntegration_MultiProviderWithKeyPoolPerProvider
// ---------------------------------------------------------------------------

func TestOpenAIIntegration_MultiProviderWithKeyPoolPerProvider(t *testing.T) {
	var mu sync.Mutex
	providerKeys := make(map[string][]string)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		var providerName string
		if strings.HasPrefix(auth, "Bearer sk-multi-a") {
			providerName = "provider-a"
		} else {
			providerName = "provider-b"
		}
		mu.Lock()
		providerKeys[providerName] = append(providerKeys[providerName], auth)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-multi")))
	}))
	defer backend.Close()

	provA := newOpenAITestProvider(t, "provider-a", backend.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backend.URL, nil)

	infos := []router.ProviderInfo{
		proxy.TestProviderInfo(provA),
		proxy.TestProviderInfo(provB),
	}

	poolA, err := keypool.NewKeyPool("provider-a", keypool.PoolConfig{
		Strategy: "round_robin",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-multi-a-1"},
			{APIKey: "sk-multi-a-2"},
		},
	})
	require.NoError(t, err)

	poolB, err := keypool.NewKeyPool("provider-b", keypool.PoolConfig{
		Strategy: "round_robin",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-multi-b-1"},
			{APIKey: "sk-multi-b-2"},
		},
	})
	require.NoError(t, err)

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderPools: func() map[string]*keypool.KeyPool {
			return map[string]*keypool.KeyPool{
				"provider-a": poolA,
				"provider-b": poolB,
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	for i := 0; i < 4; i++ {
		req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should succeed", i+1)
	}

	mu.Lock()
	defer mu.Unlock()

	aKeys := providerKeys["provider-a"]
	assert.Equal(t, 4, len(aKeys), "all 4 requests should go to provider-a")

	seen := make(map[string]int)
	for _, k := range aKeys {
		seen[k]++
	}
	assert.Equal(t, 2, len(seen), "both keys in provider-a pool should be used")
	for _, count := range seen {
		assert.Equal(t, 2, count, "each key should be used exactly twice (4 requests / 2 keys)")
	}
}
