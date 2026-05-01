package proxy

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatCompletionResponseRoundtrip(t *testing.T) {
	finishReason := "stop"
	resp := ChatCompletionResponse{
		ID:      "chatcmpl-abc123",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello, world!",
				},
				FinishReason: &finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal ChatCompletionResponse: %v", err)
	}

	var decoded ChatCompletionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal ChatCompletionResponse: %v", err)
	}

	if decoded.Object != "chat.completion" {
		t.Errorf("object = %q, want %q", decoded.Object, "chat.completion")
	}
	if decoded.ID != "chatcmpl-abc123" {
		t.Errorf("id = %q, want %q", decoded.ID, "chatcmpl-abc123")
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(decoded.Choices))
	}
	if decoded.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("content = %q, want %q", decoded.Choices[0].Message.Content, "Hello, world!")
	}
	if decoded.Choices[0].FinishReason == nil || *decoded.Choices[0].FinishReason != "stop" {
		t.Error("finish_reason should be 'stop'")
	}
	if decoded.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15", decoded.Usage.TotalTokens)
	}
}

func TestOpenAIChatCompletionChunkRoundtrip(t *testing.T) {
	chunk := ChatCompletionChunk{
		ID:      "chatcmpl-abc123",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []ChunkChoice{
			{
				Index: 0,
				Delta: Delta{
					Content: "Hello",
				},
				FinishReason: nil,
			},
		},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal ChatCompletionChunk: %v", err)
	}

	var decoded ChatCompletionChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal ChatCompletionChunk: %v", err)
	}

	if decoded.Object != "chat.completion.chunk" {
		t.Errorf("object = %q, want %q", decoded.Object, "chat.completion.chunk")
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(decoded.Choices))
	}
	if decoded.Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want %q", decoded.Choices[0].Delta.Content, "Hello")
	}
	if decoded.Choices[0].FinishReason != nil {
		t.Error("finish_reason should be nil for streaming chunk")
	}
}

func TestOpenAIErrorResponseRoundtrip(t *testing.T) {
	errResp := OpenAIErrorResponse{
		Error: OpenAIErrorDetail{
			Message: "Invalid API key",
			Type:    "invalid_request_error",
			Code:    "invalid_api_key",
		},
	}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal OpenAIErrorResponse: %v", err)
	}

	var decoded OpenAIErrorResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal OpenAIErrorResponse: %v", err)
	}

	if decoded.Error.Message != "Invalid API key" {
		t.Errorf("message = %q, want %q", decoded.Error.Message, "Invalid API key")
	}
	if decoded.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q, want %q", decoded.Error.Type, "invalid_request_error")
	}
	if decoded.Error.Code != "invalid_api_key" {
		t.Errorf("code = %q, want %q", decoded.Error.Code, "invalid_api_key")
	}

	// Verify JSON structure: {"error":{"message":...,"type":...,"code":...}}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		t.Fatal("expected 'error' to be an object")
	}
	if _, ok := errObj["message"]; !ok {
		t.Error("missing 'message' field in error object")
	}
	if _, ok := errObj["type"]; !ok {
		t.Error("missing 'type' field in error object")
	}
}

func TestOpenAIChatCompletionRequestRoundtrip(t *testing.T) {
	temp := 0.7
	maxTokens := 100
	req := ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal ChatCompletionRequest: %v", err)
	}

	var decoded ChatCompletionRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal ChatCompletionRequest: %v", err)
	}

	if decoded.Model != "gpt-4" {
		t.Errorf("model = %q, want %q", decoded.Model, "gpt-4")
	}
	if !decoded.Stream {
		t.Error("stream should be true")
	}
	if decoded.Temperature == nil || *decoded.Temperature != 0.7 {
		t.Error("temperature should be 0.7")
	}
	if decoded.MaxTokens == nil || *decoded.MaxTokens != 100 {
		t.Error("max_tokens should be 100")
	}
}

func TestOpenAIModelsResponseRoundtrip(t *testing.T) {
	resp := OpenAIModelsResponse{
		Object: "list",
		Data: []ModelObject{
			{ID: "gpt-4", Object: "model", Created: 1700000000, OwnedBy: "openai"},
			{ID: "gpt-3.5-turbo", Object: "model", Created: 1700000000, OwnedBy: "openai"},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal OpenAIModelsResponse: %v", err)
	}

	var decoded OpenAIModelsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal OpenAIModelsResponse: %v", err)
	}

	if decoded.Object != "list" {
		t.Errorf("object = %q, want %q", decoded.Object, "list")
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("data length = %d, want 2", len(decoded.Data))
	}
	if decoded.Data[0].ID != "gpt-4" {
		t.Errorf("model id = %q, want %q", decoded.Data[0].ID, "gpt-4")
	}
}

func TestOpenAISSEWriteChunk(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"id":"chatcmpl-123","object":"chat.completion.chunk"}`)

	WriteOpenAISSEChunk(&buf, payload)

	expected := "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\"}\n\n"
	if buf.String() != expected {
		t.Errorf("SSE chunk = %q, want %q", buf.String(), expected)
	}
}

func TestOpenAISSEWriteDone(t *testing.T) {
	var buf bytes.Buffer

	WriteOpenAISSEDone(&buf)

	expected := "data: [DONE]\n\n"
	if buf.String() != expected {
		t.Errorf("SSE done = %q, want %q", buf.String(), expected)
	}
}

func TestOpenAISSESetHeaders(t *testing.T) {
	w := httptest.NewRecorder()

	SetOpenAISSEHeaders(w)

	headers := w.Header()
	if ct := headers.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}
	if cc := headers.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}
	if xab := headers.Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want %q", xab, "no")
	}
}

func TestOpenAIIsStreamingRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "stream true no space",
			body: `{"model":"gpt-4","stream":true}`,
			want: true,
		},
		{
			name: "stream true with space",
			body: `{"model":"gpt-4", "stream": true}`,
			want: true,
		},
		{
			name: "stream false",
			body: `{"model":"gpt-4","stream":false}`,
			want: false,
		},
		{
			name: "no stream field",
			body: `{"model":"gpt-4"}`,
			want: false,
		},
		{
			name: "invalid JSON",
			body: `not json`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OpenAIGetIsStreaming([]byte(tt.body))
			if got != tt.want {
				t.Errorf("IsStreamingRequest(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestOpenAISSEChunkMultipleChunks(t *testing.T) {
	var buf bytes.Buffer

	chunk1 := []byte(`{"choices":[{"delta":{"content":"Hello"}}]}`)
	chunk2 := []byte(`{"choices":[{"delta":{"content":" world"}}]}`)

	WriteOpenAISSEChunk(&buf, chunk1)
	WriteOpenAISSEChunk(&buf, chunk2)
	WriteOpenAISSEDone(&buf)

	output := buf.String()

	// Should have two data chunks and one [DONE]
	dataLines := strings.Count(output, "data: ")
	if dataLines != 3 {
		t.Errorf("expected 3 data lines, got %d", dataLines)
	}
	if !strings.Contains(output, "data: [DONE]\n\n") {
		t.Error("missing [DONE] terminator")
	}
}

// OpenAIGetIsStreaming wraps the OpenAI-specific IsStreamingRequest for testing.
// We use the function from openai_sse.go directly.
func TestOpenAIFinishReasonNilOmit(t *testing.T) {
	// Verify that nil FinishReason is properly omitted in JSON
	chunk := ChatCompletionChunk{
		ID:      "chatcmpl-123",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []ChunkChoice{
			{Index: 0, Delta: Delta{Content: "Hi"}},
		},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// finish_reason should be null or omitted (it's *string with omitempty won't omit null,
	// but *string nil does serialize as null — that's fine for OpenAI spec)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	choices := raw["choices"].([]any)
	choice := choices[0].(map[string]any)
	// OpenAI spec: finish_reason can be null during streaming
	if fr, ok := choice["finish_reason"]; ok && fr != nil {
		t.Errorf("finish_reason = %v, want null", fr)
	}
}
