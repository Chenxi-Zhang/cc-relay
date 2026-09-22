package proxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesPassthrough_ModelMappingOnlyChangesModelAndUsesRelayKey(t *testing.T) {
	t.Parallel()

	request := map[string]any{
		"model": "client-model",
		"input": []map[string]any{{
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": "hello",
			}},
		}},
		"tools": []map[string]any{{
			"type": "function",
			"name": "lookup",
		}},
		"metadata": map[string]any{
			"trace_id": "trace-123",
		},
		"custom_field": map[string]any{
			"nested": true,
		},
		"stream": true,
	}
	requestBody, err := json.Marshal(request)
	require.NoError(t, err)

	var received map[string]any
	var receivedPath string
	var receivedAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n"))
	}))
	defer backend.Close()

	mapping := map[string]string{"client-model": "upstream-model"}
	rec := serveResponsesPassthrough(
		t, newResponsesPassthroughMux(t, backend.URL, mapping), string(requestBody),
	)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	assert.Equal(t, "/v1/responses", receivedPath)
	assert.Equal(t, "Bearer sk-upstream", receivedAuth)

	var expected map[string]any
	require.NoError(t, json.Unmarshal(requestBody, &expected))
	expected["model"] = "upstream-model"
	assert.Equal(t, expected, received)
	assert.Equal(t, true, received["stream"])
}
