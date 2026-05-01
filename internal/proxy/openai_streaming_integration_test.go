package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SSE test helpers
// ---------------------------------------------------------------------------

// sseBackend creates a test backend that sends the given SSE chunks sequentially,
// then closes. Each chunk should be a full SSE line like "data: {...}\n\n".
func sseBackend(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range chunks {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
}

// parseSSELines extracts all "data: ..." lines from an SSE response body.
// It returns only the data content (without the "data: " prefix).
func parseSSELines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			lines = append(lines, strings.TrimPrefix(line, "data: "))
		}
	}
	return lines
}

// streamingRequest creates a streaming chat completions request.
func streamingRequest(t *testing.T, model string) *http.Request {
	t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
	return newOpenAIChatRequest(t, body)
}

// ---------------------------------------------------------------------------
// Integration tests — streaming + edge cases
// ---------------------------------------------------------------------------

func TestOpenAIStreaming_MultiChunkPassthrough(t *testing.T) {
	chunks := []string{
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"! How\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" can I\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" help?\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}

	backend := sseBackend(t, chunks)
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := streamingRequest(t, "gpt-4")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	body := rec.Body.String()
	sseLines := parseSSELines(body)

	// Should have 5 data chunks + [DONE]
	require.Len(t, sseLines, 6, "expected 5 data chunks + [DONE], got SSE lines: %v", sseLines)

	// Verify the 5 JSON chunks in order
	for i := 0; i < 5; i++ {
		var chunk map[string]any
		require.NoError(t, json.Unmarshal([]byte(sseLines[i]), &chunk),
			"chunk %d should be valid JSON: %s", i, sseLines[i])
		assert.Equal(t, "chatcmpl-1", chunk["id"])
	}

	// Last SSE line should be [DONE]
	assert.Equal(t, "[DONE]", sseLines[5])
}

func TestOpenAIStreaming_ModelMappingInStream(t *testing.T) {
	var receivedModel string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the model from the incoming request body.
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		receivedModel, _ = req["model"].(string)

		// Respond with SSE stream.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-map\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	mapping := map[string]string{
		"gpt-4o": "deepseek-chat",
	}
	handler := newOpenAIHandler(t, backend.URL, mapping, openaiTestKey)

	req := streamingRequest(t, "gpt-4o")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "deepseek-chat", receivedModel,
		"model should be mapped from gpt-4o to deepseek-chat in streaming request")
}

func TestOpenAIStreaming_SSEFormatValidation(t *testing.T) {
	chunk1 := "data: {\"id\":\"fmt-1\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"
	chunk2 := "data: {\"id\":\"fmt-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	done := "data: [DONE]\n\n"

	backend := sseBackend(t, []string{chunk1, chunk2, done})
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := streamingRequest(t, "gpt-4")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()

	// Split by double newline to get individual SSE events.
	events := strings.Split(body, "\n\n")
	// Filter out empty trailing entries.
	var nonEmpty []string
	for _, ev := range events {
		if strings.TrimSpace(ev) != "" {
			nonEmpty = append(nonEmpty, ev)
		}
	}
	require.Len(t, nonEmpty, 3, "expected 3 SSE events (2 data + 1 DONE), got: %v", nonEmpty)

	// First two events should be valid JSON after "data: " prefix.
	for i := 0; i < 2; i++ {
		dataLine := nonEmpty[i]
		assert.True(t, strings.HasPrefix(dataLine, "data: "),
			"event %d should start with 'data: ': %q", i, dataLine)

		jsonPayload := strings.TrimPrefix(dataLine, "data: ")
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(jsonPayload), &parsed),
			"event %d payload should be valid JSON: %s", i, jsonPayload)
	}

	// Third event should be [DONE].
	assert.Equal(t, "data: [DONE]", nonEmpty[2])
}

func TestOpenAIStreaming_BackendError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error","code":"internal"}}`))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := streamingRequest(t, "gpt-4")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The error response should be 500.
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Error responses should be JSON, not SSE.
	contentType := rec.Header().Get("Content-Type")
	assert.Equal(t, "application/json", contentType,
		"error response should be JSON, not SSE")

	// Verify the error body is valid OpenAI error format.
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "internal server error", errResp.Error.Message)
}

func TestOpenAIStreaming_ConcurrentRequests(t *testing.T) {
	var counter atomic.Int32

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := counter.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"chatcmpl-concurrent-" + json.Number(strings.TrimSpace(strings.Replace(
			string(rune('0'+n%10)), string(rune('0'+n%10)), string(rune('0'+n%10)), 1))) +
			"\",\"choices\":[{\"delta\":{\"content\":\"response\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := streamingRequest(t, "gpt-4")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			results <- rec
		}()
	}
	wg.Wait()
	close(results)

	count := 0
	for rec := range results {
		count++
		assert.Equal(t, http.StatusOK, rec.Code, "concurrent request %d should succeed", count)

		body := rec.Body.String()
		sseLines := parseSSELines(body)
		require.NotEmpty(t, sseLines, "concurrent request %d should have SSE data", count)
		assert.Contains(t, body, "data: [DONE]",
			"concurrent request %d should end with [DONE]", count)
	}
	assert.Equal(t, 3, count, "all 3 concurrent requests should complete")
}

func TestOpenAIStreaming_LargeResponse(t *testing.T) {
	// Create a 10KB content string.
	largeContent := strings.Repeat("A", 10*1024)

	// Build the SSE chunk with large content.
	largeChunk := map[string]any{
		"id":      "chatcmpl-large",
		"object":  "chat.completion.chunk",
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"content": largeContent,
				},
				"finish_reason": nil,
			},
		},
	}
	largeJSON, err := json.Marshal(largeChunk)
	require.NoError(t, err)

	chunks := []string{
		"data: " + string(largeJSON) + "\n\n",
		"data: [DONE]\n\n",
	}

	backend := sseBackend(t, chunks)
	defer backend.Close()

	handler := newOpenAIHandler(t, backend.URL, nil, openaiTestKey)

	req := streamingRequest(t, "gpt-4")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	sseLines := parseSSELines(body)
	require.Len(t, sseLines, 2, "expected 1 data chunk + [DONE]")

	// The first line should parse as valid JSON containing the full 10KB content.
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(sseLines[0]), &chunk))
	require.Len(t, chunk.Choices, 1)
	assert.Equal(t, largeContent, chunk.Choices[0].Delta.Content,
		"large content should not be truncated")
	assert.Len(t, chunk.Choices[0].Delta.Content, 10*1024,
		"content should be exactly 10240 bytes")

	// Second line is [DONE].
	assert.Equal(t, "[DONE]", sseLines[1])
}
