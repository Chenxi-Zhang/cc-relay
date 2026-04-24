package proxy

import (
	"bytes"
	"io"
	"net/http"

	"github.com/tidwall/gjson"
)

// peekWriter wraps an http.ResponseWriter and buffers 429 responses for retry.
// For non-429 responses, it immediately commits to the real writer and switches
// to direct pass-through mode. This enables retry of streaming requests when the
// first response from the backend is a 429, without buffering successful SSE streams.
//
// The httputil.ReverseProxy calls modifyResponse before writing anything to the
// ResponseWriter. For a 429, the backend returns a complete small JSON error — no
// SSE data is ever sent. So buffering is safe and memory-bounded (< 1KB).
type peekWriter struct {
	real        http.ResponseWriter
	header      http.Header
	body        bytes.Buffer
	statusCode  int
	committed   bool
	wroteHeader bool
	is429       bool
}

func newPeekWriter(real http.ResponseWriter) *peekWriter {
	return &peekWriter{
		real:   real,
		header: make(http.Header),
	}
}

func (pw *peekWriter) Header() http.Header {
	if pw.committed {
		return pw.real.Header()
	}
	return pw.header
}

func (pw *peekWriter) WriteHeader(code int) {
	if pw.wroteHeader {
		return
	}
	pw.wroteHeader = true
	pw.statusCode = code
	if code == http.StatusTooManyRequests {
		pw.is429 = true
		return
	}
	pw.commit()
}

func (pw *peekWriter) Write(data []byte) (int, error) {
	if pw.committed {
		return pw.real.Write(data)
	}
	// Implicit 200 if Write called before WriteHeader.
	if pw.statusCode == 0 {
		pw.statusCode = http.StatusOK
		pw.commit()
		return pw.real.Write(data)
	}
	return pw.body.Write(data)
}

// Flush implements http.Flusher. In peek mode it is a no-op; in direct mode
// it forwards to the real writer.
func (pw *peekWriter) Flush() {
	if pw.committed {
		if flusher, ok := pw.real.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// Is429 returns true if the response was a 429 that is still buffered.
func (pw *peekWriter) Is429() bool { return pw.is429 }

// IsCommitted returns true after the response has been sent to the real writer.
func (pw *peekWriter) IsCommitted() bool { return pw.committed }

// Status returns the buffered status code, or 0 if WriteHeader was never called.
func (pw *peekWriter) Status() int { return pw.statusCode }

// FlushBuffered429 writes the buffered 429 response to the real writer.
// Used when all retry attempts are exhausted.
func (pw *peekWriter) FlushBuffered429() {
	if pw.committed || !pw.is429 {
		return
	}
	pw.commit()
}

// commit copies all buffered headers and body to the real writer.
func (pw *peekWriter) commit() {
	if pw.committed {
		return
	}

	// Copy buffered headers to real writer.
	dst := pw.real.Header()
	for k, vv := range pw.header {
		dst[k] = vv
	}

	code := pw.statusCode
	if code == 0 {
		code = http.StatusOK
	}
	pw.real.WriteHeader(code)

	if pw.body.Len() > 0 {
		pw.body.WriteTo(pw.real)
	}

	pw.committed = true
}

// maxPeekBytes is the upper limit for how many bytes peekModelFromSSE will read
// before giving up on finding a model field.
const maxPeekBytes = 4096

// peekModelFromSSE reads from r incrementally, looking for the "model" field
// inside an SSE "message_start" data payload. It returns the model name and all
// bytes read (which must be replayed into the response body). If the model is
// not found within maxPeekBytes, it returns ("", peeked).
func peekModelFromSSE(r io.Reader) (model string, peeked []byte) {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for len(buf) < maxPeekBytes {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if n > 0 {
			// Look for "data:" lines containing "message_start"
			for _, line := range bytes.Split(buf, []byte("\n")) {
				line = bytes.TrimSpace(line)
				after, ok := bytes.CutPrefix(line, []byte("data:"))
				if !ok {
					continue
				}
				after = bytes.TrimSpace(after)
				if !bytes.Contains(after, []byte("message_start")) {
					continue
				}
				// Extract model from JSON: message.model
				m := gjson.GetBytes(after, "message.model")
				if m.Exists() {
					return m.String(), buf
				}
			}
		}
		if err != nil {
			break
		}
	}
	return "", buf
}
