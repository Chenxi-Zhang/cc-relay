package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeekWriter_WriteHeader200_CommitsImmediately(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.Header().Set("Content-Type", "text/event-stream")
	pw.WriteHeader(http.StatusOK)
	_, err := pw.Write([]byte("event: ping\ndata: {}\n\n"))
	require.NoError(t, err)

	assert.True(t, pw.IsCommitted())
	assert.False(t, pw.Is429())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "event: ping\ndata: {}\n\n", rec.Body.String())
}

func TestPeekWriter_WriteHeader429_StaysBuffered(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.Header().Set("Retry-After", "5")
	pw.WriteHeader(http.StatusTooManyRequests)
	_, err := pw.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`))
	require.NoError(t, err)

	assert.True(t, pw.Is429())
	assert.False(t, pw.IsCommitted())
	assert.Empty(t, rec.Body.String())
	assert.Empty(t, rec.Header().Get("Retry-After"))
}

func TestPeekWriter_FlushBuffered429(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.Header().Set("Retry-After", "5")
	pw.WriteHeader(http.StatusTooManyRequests)
	_, err := pw.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error"}}`))
	require.NoError(t, err)

	pw.FlushBuffered429()

	assert.True(t, pw.IsCommitted())
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "rate_limit_error")
}

func TestPeekWriter_WriteBeforeWriteHeader_Implicit200(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	_, err := pw.Write([]byte("hello"))
	require.NoError(t, err)

	assert.True(t, pw.IsCommitted())
	assert.False(t, pw.Is429())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())
}

func TestPeekWriter_HeadersPreservedOnCommit(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.Header().Set("X-Custom", "value1")
	pw.Header().Set("Content-Type", "application/json")
	pw.WriteHeader(http.StatusOK)

	assert.Equal(t, "value1", rec.Header().Get("X-Custom"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestPeekWriter_FlushInPeekMode_NoOp(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.WriteHeader(http.StatusTooManyRequests)

	// Should not panic or change state.
	pw.Flush()

	assert.False(t, pw.IsCommitted())
	assert.True(t, pw.Is429())
}

func TestPeekWriter_FlushInDirectMode_Forwarded(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.WriteHeader(http.StatusOK)

	// After commit, Flush should forward to real writer (no panic).
	assert.NotPanics(t, func() {
		pw.Flush()
	})
}

func TestPeekWriter_DoubleWriteHeader_NoOp(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.WriteHeader(http.StatusTooManyRequests)
	assert.True(t, pw.Is429())
	assert.Equal(t, http.StatusTooManyRequests, pw.Status())

	// Second WriteHeader should be ignored.
	pw.WriteHeader(http.StatusOK)
	assert.True(t, pw.Is429())
	assert.False(t, pw.IsCommitted())
	assert.Empty(t, rec.Body.String())
}

func TestPeekWriter_StatusReturnsBufferedCode(t *testing.T) {
	t.Parallel()

	pw := newPeekWriter(httptest.NewRecorder())

	assert.Equal(t, 0, pw.Status())

	pw.WriteHeader(http.StatusBadGateway)
	assert.Equal(t, http.StatusBadGateway, pw.Status())
	assert.True(t, pw.IsCommitted())
}

func TestPeekWriter_ImplementsInterfaces(t *testing.T) {
	t.Parallel()

	pw := newPeekWriter(httptest.NewRecorder())

	// Compile-time interface checks.
	var _ http.ResponseWriter = pw
	var _ http.Flusher = pw
}

func TestPeekWriter_FlushBuffered429_Not429_NoOp(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	pw := newPeekWriter(rec)

	pw.WriteHeader(http.StatusOK)
	// Already committed, FlushBuffered429 should be a no-op.
	pw.FlushBuffered429()
	assert.True(t, pw.IsCommitted())
}
