package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesPassthrough_JSONResponseIsUnchanged(t *testing.T) {
	t.Parallel()

	const requestBody = `{"model":"gpt-test","input":"hello","stream":false}`
	const responseBody = `{"id":"resp_json","object":"response","created_at":123,"status":"completed","output":[]}`

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Response", "preserved")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer backend.Close()

	rec := serveResponsesPassthrough(
		t, newResponsesPassthroughMux(t, backend.URL, nil), requestBody,
	)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "preserved", rec.Header().Get("X-Upstream-Response"))
	assert.Equal(t, responseBody, rec.Body.String())
}

func TestResponsesPassthrough_SSEResponseIsUnchanged(t *testing.T) {
	t.Parallel()

	const requestBody = `{"model":"gpt-test","input":"hello","stream":true}`
	const responseBody = "event: response.created\n" +
		`data: {"type":"response.created"}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed"}` + "\n\n"

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(responseBody))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer backend.Close()

	rec := serveResponsesPassthrough(
		t, newResponsesPassthroughMux(t, backend.URL, nil), requestBody,
	)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, responseBody, rec.Body.String())
}

func TestResponsesPassthrough_UpstreamErrorIsUnchanged(t *testing.T) {
	t.Parallel()

	const requestBody = `{"model":"gpt-test","input":"hello","stream":false}`
	const responseBody = `{"error":{"message":"response conflict","type":"invalid_request_error","code":"conflict"}}`

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer backend.Close()

	rec := serveResponsesPassthrough(
		t, newResponsesPassthroughMux(t, backend.URL, nil), requestBody,
	)

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, responseBody, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "failed to convert")
}
