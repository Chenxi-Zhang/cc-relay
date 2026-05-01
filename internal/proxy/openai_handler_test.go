package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

const (
	openaiTestKey    = "sk-test-openai-key"
	openaiProvider   = "openai-test"
	openaiChatPath   = "/openai/v1/chat/completions"
)

func newOpenAIChatRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, openaiChatPath,
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newOpenAITestProvider(t *testing.T, name, baseURL string, modelMapping map[string]string) providers.Provider {
	t.Helper()
	return providers.NewOpenAIProviderWithMapping(
		name, baseURL,
		[]string{"gpt-4o", "gpt-4", "gpt-3.5-turbo"},
		modelMapping,
	)
}

func newOpenAIHandler(
	t *testing.T,
	backendURL string,
	modelMapping map[string]string,
	apiKey string,
) *proxy.OpenAIHandler {
	t.Helper()
	prov := newOpenAITestProvider(t, openaiProvider, backendURL, modelMapping)
	infos := []router.ProviderInfo{
		proxy.TestProviderInfo(prov),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:           router.NewFailoverRouter(0),
		Providers:        func() []router.ProviderInfo { return infos },
		GetProviderPools: nil,
		GetProviderKeys:  func() map[string]string { return map[string]string{openaiProvider: apiKey} },
		DebugOptions:     proxy.TestDebugOptions(),
	})
	require.NoError(t, err)
	return h
}

func newOpenAIHandlerWithPool(
	t *testing.T,
	backendURL string,
	pool *keypool.KeyPool,
) *proxy.OpenAIHandler {
	t.Helper()
	prov := newOpenAITestProvider(t, openaiProvider, backendURL, nil)
	infos := []router.ProviderInfo{
		proxy.TestProviderInfo(prov),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderPools: func() map[string]*keypool.KeyPool {
			return map[string]*keypool.KeyPool{openaiProvider: pool}
		},
		GetProviderKeys:  nil,
		DebugOptions:     proxy.TestDebugOptions(),
	})
	require.NoError(t, err)
	return h
}

func TestOpenAIHandlerNonStreamingSuccess(t *testing.T) {
	backendResp := `{"id":"chatcmpl-123","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+openaiTestKey, r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(backendResp))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-123", resp["id"])
}

func TestOpenAIHandlerModelMapping(t *testing.T) {
	var receivedModel string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		receivedModel, _ = req["model"].(string)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-mapped","object":"chat.completion","choices":[]}`))
	}))
	defer backend.Close()

	mapping := map[string]string{
		"gpt-4o": "deepseek-chat",
	}
	handler := newOpenAIHandler(t, backend.URL, mapping, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "deepseek-chat", receivedModel, "model should be mapped from gpt-4o to deepseek-chat")
}

func TestOpenAIHandlerNoProviders(t *testing.T) {
	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return nil },
		GetProviderPools: nil,
		GetProviderKeys:  nil,
		DebugOptions:     proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler := h
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "no openai providers available", errResp.Error.Message)
	assert.Equal(t, "server_error", errResp.Error.Type)
	assert.Equal(t, "no_providers", errResp.Error.Code)
}

func TestOpenAIHandlerStreamingSuccess(t *testing.T) {
	sseChunks := "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: [DONE]\n\n"

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+openaiTestKey, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseChunks))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	assert.Contains(t, body, "data: {\"id\":\"chatcmpl-1\"")
	assert.Contains(t, body, "data: [DONE]")
}

func TestOpenAIHandlerStreamingSSEHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache, no-transform", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
}

func TestOpenAIHandlerNonStreamingStillWorks(t *testing.T) {
	backendResp := `{"id":"chatcmpl-ns","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hi!"},"finish_reason":"stop"}]}`

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(backendResp))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotEqual(t, "text/event-stream", rec.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-ns", resp["id"])
}

func TestOpenAIHandlerInvalidBody(t *testing.T) {
	h := newOpenAIHandler(t, "http://127.0.0.1:1", nil, openaiTestKey)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, openaiChatPath,
		strings.NewReader(""),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	_ = rec.Code
}

func TestOpenAIHandlerInvalidBodyReadError(t *testing.T) {
	h := newOpenAIHandler(t, "http://127.0.0.1:1", nil, openaiTestKey)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, openaiChatPath,
		&openaiErrorReader{},
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "invalid_request_error", errResp.Error.Type)
}

func TestOpenAIHandlerKeySelection(t *testing.T) {
	var authHeader string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-keytest","object":"chat.completion","choices":[]}`))
	}))
	defer backend.Close()

	pool, err := keypool.NewKeyPool(openaiProvider, keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-pool-key-1"},
			{APIKey: "sk-pool-key-2"},
		},
	})
	require.NoError(t, err)

	handler := newOpenAIHandlerWithPool(t, backend.URL, pool)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, strings.HasPrefix(authHeader, "Bearer sk-pool-key-"), "expected pool key to be used, got: %s", authHeader)

	assert.NotEmpty(t, rec.Header().Get("X-CC-Relay-Key-ID"))
	assert.Equal(t, "2", rec.Header().Get("X-CC-Relay-Keys-Total"))
	assert.Equal(t, "2", rec.Header().Get("X-CC-Relay-Keys-Available"))
}

func TestOpenAIHandlerNewHandlerValidation(t *testing.T) {
	t.Run("nil options", func(t *testing.T) {
		_, err := proxy.NewOpenAIHandler(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "options are required")
	})

	t.Run("nil router", func(t *testing.T) {
		_, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
			Router:       nil,
			Providers:    func() []router.ProviderInfo { return nil },
			DebugOptions: proxy.TestDebugOptions(),
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "router is required")
	})

	t.Run("nil providers func", func(t *testing.T) {
		_, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
			Router:       router.NewFailoverRouter(0),
			Providers:    nil,
			DebugOptions: proxy.TestDebugOptions(),
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "providers function is required")
	})
}

func TestOpenAIHandlerMultipleProviders(t *testing.T) {
	var selectedBackend string

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedBackend = "A"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-a","object":"chat.completion","choices":[]}`))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedBackend = "B"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-b","object":"chat.completion","choices":[]}`))
	}))
	defer backendB.Close()

	provA := newOpenAITestProvider(t, "provider-a", backendA.URL, nil)
	provB := newOpenAITestProvider(t, "provider-b", backendB.URL, nil)

	infos := []router.ProviderInfo{
		proxy.TestProviderInfo(provA),
		proxy.TestProviderInfo(provB),
	}

	h, err := proxy.NewOpenAIHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewRoundRobinRouter(),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{
				"provider-a": "key-a",
				"provider-b": "key-b",
			}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "A", selectedBackend)
}

func TestOpenAIHandlerPathRewrite(t *testing.T) {
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(openAICompletionResponse, "chatcmpl-path")))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := newOpenAIChatRequest(t, `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/chat/completions", receivedPath,
		"/openai/v1 prefix must be stripped before proxying to backend")
}

type openaiErrorReader struct{}

func (e *openaiErrorReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (e *openaiErrorReader) Close() error {
	return nil
}
