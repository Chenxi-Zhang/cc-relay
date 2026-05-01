package proxy

import (
	"encoding/json"
	"io"
	"net/http"
)

func SetOpenAISSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
}

func WriteOpenAISSEChunk(w io.Writer, data []byte) {
	w.Write([]byte("data: "))
	w.Write(data)
	w.Write([]byte("\n\n"))
}

func WriteOpenAISSEDone(w io.Writer) {
	w.Write([]byte("data: [DONE]\n\n"))
}

func OpenAIGetIsStreaming(body []byte) bool {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	stream, ok := req["stream"].(bool)
	return ok && stream
}
