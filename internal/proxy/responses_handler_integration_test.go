package proxy_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

const chatCompletionResponseJSON = `{"id":"chatcmpl-test-123","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from upstream!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func newResponsesTestProvider(t *testing.T, name, baseURL string) providers.Provider {
	t.Helper()
	return providers.NewOpenAIProviderWithMapping(
		name, baseURL,
		[]string{"gpt-4", "gpt-3.5-turbo"},
		nil,
	)
}

func newResponsesHandler(t *testing.T, provs ...providers.Provider) *proxy.ResponsesHandler {
	t.Helper()
	infos := make([]router.ProviderInfo, len(provs))
	for i, p := range provs {
		infos[i] = proxy.TestProviderInfo(p)
	}
	h, err := proxy.NewResponsesHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			keys := make(map[string]string, len(provs))
			for _, p := range provs {
				keys[p.Name()] = "sk-test-key"
			}
			return keys
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)
	return h
}

func newResponsesRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestResponsesIntegration_NonStreaming_HappyPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-test-key", r.Header.Get("Authorization"))

		var reqBody map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
		assert.Equal(t, "gpt-4", reqBody["model"])

		messages, ok := reqBody["messages"].([]any)
		require.True(t, ok)
		assert.Len(t, messages, 1)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatCompletionResponseJSON)
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"Hello"}}]}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "response", resp["object"])
	assert.Contains(t, resp, "id")
	assert.Equal(t, "completed", resp["status"])
	assert.Contains(t, resp, "model")
	assert.Contains(t, resp, "usage")

	output, ok := resp["output"].([]any)
	require.True(t, ok)
	require.Len(t, output, 1)

	item, ok := output[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "message", item["type"])

	msg, ok := item["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", msg["role"])

	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	part, ok := content[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "output_text", part["type"])
	assert.Equal(t, "Hello from upstream!", part["text"])
}

func TestResponsesIntegration_NonStreaming_ToolCalling(t *testing.T) {
	toolCallResponse := `{"id":"chatcmpl-tools","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_abc123","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":10,"total_tokens":25}}`

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, toolCallResponse)
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBodyMap := map[string]any{
		"model": "gpt-4",
		"input": []map[string]any{
			{"type": "message", "message": map[string]any{"role": "user", "content": "What is the weather?"}},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "get_weather",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"city": map[string]any{"type": "string"}},
					},
				},
			},
		},
	}
	reqJSON, err := json.Marshal(reqBodyMap)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBuffer(reqJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !assert.Equal(t, http.StatusOK, rec.Code) {
		t.Logf("Response body: %s", rec.Body.String())
		t.FailNow()
	}

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "completed", resp["status"])

	output, ok := resp["output"].([]any)
	require.True(t, ok)
	assert.Len(t, output, 2)

	msgItem, ok := output[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "message", msgItem["type"])

	toolItem, ok := output[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call", toolItem["type"])

	fc, ok := toolItem["function_call"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "get_weather", fc["name"])
	assert.Equal(t, `{"city":"Paris"}`, fc["arguments"])
	assert.Equal(t, "call_abc123", fc["call_id"])
}

func TestResponsesIntegration_Streaming_HappyPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		chunks := []string{
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n",
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n",
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n",
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		}

		for _, chunk := range chunks {
			fmt.Fprint(w, chunk)
			flusher.Flush()
		}
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"Say hello"}}],"stream":true}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	assert.Contains(t, body, "event: response.created")
	assert.Contains(t, body, "event: response.output_item.added")
	assert.Contains(t, body, "event: response.output_text.delta")
	assert.Contains(t, body, "event: response.completed")

	assert.Contains(t, body, `"delta":"Hel"`)
	assert.Contains(t, body, `"delta":"lo"`)
	assert.Contains(t, body, `"delta":"!"`)
}

func TestResponsesIntegration_UpstreamError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"model overloaded","type":"server_error","code":"overloaded"}}`)
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"hi"}}]}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, errObj["message"], "model overloaded")
}

func TestResponsesIntegration_NoProviders(t *testing.T) {
	handler, err := proxy.NewResponsesHandler(&proxy.OpenAIHandlerOptions{
		Router:    router.NewFailoverRouter(0),
		Providers: func() []router.ProviderInfo { return nil },
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"hi"}}]}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "server_error", errObj["type"])
}

func TestResponsesIntegration_RequestConversion(t *testing.T) {
	var receivedBody map[string]any

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatCompletionResponseJSON)
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"Hello"}},{"type":"function_call_output","function_call_output":{"call_id":"call_123","output":"Temperature is 22C"}}],"temperature":0.7,"max_output_tokens":100,"top_p":0.9}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "gpt-4", receivedBody["model"])

	messages, ok := receivedBody["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 2)

	msg1, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", msg1["role"])
	assert.Equal(t, "Hello", msg1["content"])

	msg2, ok := messages[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool", msg2["role"])
	assert.Equal(t, "Temperature is 22C", msg2["content"])
	assert.Equal(t, "call_123", msg2["tool_call_id"])

	assert.InDelta(t, 0.7, receivedBody["temperature"], 0.001)

	maxTokens, ok := receivedBody["max_tokens"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(100), maxTokens)
}

func TestResponsesIntegration_Streaming_ParsesSSEEvents(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-s\",\"choices\":[{\"delta\":{\"content\":\"test\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-s\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer backend.Close()

	prov := newResponsesTestProvider(t, "test-provider", backend.URL)
	handler := newResponsesHandler(t, prov)

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"hi"}}],"stream":true}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	scanner := bufio.NewScanner(rec.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}

	assert.Contains(t, events, "response.created")
	assert.Contains(t, events, "response.in_progress")
	assert.Contains(t, events, "response.output_item.added")
	assert.Contains(t, events, "response.output_text.delta")
	assert.Contains(t, events, "response.completed")
}

func TestResponsesIntegration_FailoverBetweenProviders(t *testing.T) {
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-backend-b","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from B!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer backendB.Close()

	provA := newResponsesTestProvider(t, "provider-a", backendA.URL)
	provB := newResponsesTestProvider(t, "provider-b", backendB.URL)

	infos := []router.ProviderInfo{
		proxy.TestProviderInfoWithHealth(provA, func() bool { return false }),
		proxy.TestProviderInfoWithHealth(provB, func() bool { return true }),
	}

	handler, err := proxy.NewResponsesHandler(&proxy.OpenAIHandlerOptions{
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

	reqBody := `{"model":"gpt-4","input":[{"type":"message","message":{"role":"user","content":"hi"}}]}`
	req := newResponsesRequest(t, reqBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "response", resp["object"])

	output := resp["output"].([]any)
	msgItem := output[0].(map[string]any)
	contentParts := msgItem["message"].(map[string]any)["content"].([]any)
	part, ok := contentParts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Hello from B!", part["text"])
}
