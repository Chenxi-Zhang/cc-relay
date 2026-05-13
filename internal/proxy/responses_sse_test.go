package proxy

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamConverter_NewStreamConverter(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	require.NotNil(t, converter)
	assert.NotEmpty(t, converter.responseID)
	assert.Equal(t, 0, len(converter.outputItems))
	assert.Nil(t, converter.currentItem)
	assert.Equal(t, "", converter.contentBuffer)
	assert.Equal(t, buf, converter.eventWriter)
}

func TestStreamConverter_ProcessChunk_FirstChunkCreatesEvent(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk)

	require.NoError(t, err)

	assert.Equal(t, 1, len(converter.outputItems))
	assert.NotNil(t, converter.currentItem)
	assert.Equal(t, "message", converter.currentItem.Type)

	output := buf.String()
	assert.Contains(t, output, "event: response.created")
	assert.Contains(t, output, "event: response.in_progress")
	assert.Contains(t, output, "data:")
	assert.Contains(t, output, "\"type\":\"response.created\"")
	assert.Contains(t, output, "\"response\":{")
	assert.Contains(t, output, "\"object\":\"response\"")
}

func TestStreamConverter_ProcessChunk_ContentDelta(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk1 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk1)
	require.NoError(t, err)

	buf.Reset()

	chunk2 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n")
	err = converter.ProcessChunk(chunk2)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "event: response.output_text.delta")
	assert.Contains(t, output, "\"type\":\"response.output_text.delta\"")
	assert.Contains(t, output, "\"delta\":\" world\"")

	assert.Equal(t, "Hello world", converter.contentBuffer)
}

func TestStreamConverter_ProcessChunk_FinishReason(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk1 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk1)
	require.NoError(t, err)

	buf.Reset()

	chunk2 := []byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	err = converter.ProcessChunk(chunk2)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "event: response.output_text.done")
	assert.Contains(t, output, "event: response.output_item.done")
	assert.Contains(t, output, "event: response.completed")
	assert.Contains(t, output, "\"type\":\"response.completed\"")
	assert.Contains(t, output, "\"status\":\"completed\"")
}

func TestStreamConverter_ProcessChunk_LengthReason(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk1 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk1)
	require.NoError(t, err)

	buf.Reset()

	chunk2 := []byte("data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\n")
	err = converter.ProcessChunk(chunk2)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "event: response.completed")
	assert.Contains(t, output, "\"type\":\"response.completed\"")
	assert.Contains(t, output, "\"status\":\"incomplete\"")
}

func TestStreamConverter_ProcessChunk_DONEEvent(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk1 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk1)
	require.NoError(t, err)

	buf.Reset()

	doneChunk := []byte("data: [DONE]\n\n")
	err = converter.ProcessChunk(doneChunk)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "event: response.completed")
	assert.Contains(t, output, "\"type\":\"response.completed\"")
	assert.Contains(t, output, "\"status\":\"completed\"")
}

func TestStreamConverter_ProcessChunk_InvalidChunkFormat(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("invalid chunk data\n\n")

	err := converter.ProcessChunk(chunk)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid SSE chunk format")
}

func TestStreamConverter_ProcessChunk_InvalidJSON(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("data: {invalid json}\n\n")

	err := converter.ProcessChunk(chunk)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse SSE chunk")
}

func TestStreamConverter_Reset(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	_ = converter.ProcessChunk(chunk)

	assert.NotEqual(t, "", converter.contentBuffer)
	assert.NotNil(t, converter.currentItem)
	assert.Equal(t, 1, len(converter.outputItems))

	converter.Reset()

	assert.Equal(t, "", converter.contentBuffer)
	assert.Nil(t, converter.currentItem)
	assert.Equal(t, 0, len(converter.outputItems))
	assert.NotEqual(t, "", converter.responseID)
	assert.Equal(t, false, converter.completed)
	assert.Equal(t, "", converter.model)
}

func TestStreamConverter_EmptyContent(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n")
	err := converter.ProcessChunk(chunk)

	require.NoError(t, err)

	assert.Equal(t, 0, len(converter.outputItems))
	assert.Nil(t, converter.currentItem)
	assert.Equal(t, "", converter.contentBuffer)
}

func TestStreamConverter_MultipleDeltas(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n",
	}

	for _, chunkData := range chunks {
		chunk := []byte(chunkData)
		err := converter.ProcessChunk(chunk)
		require.NoError(t, err)
	}

	assert.Equal(t, "Hello world", converter.contentBuffer)
	assert.Equal(t, 1, len(converter.outputItems))
}

func TestStreamConverter_SSEEventFormatting(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk)

	require.NoError(t, err)

	output := buf.String()

	assert.Contains(t, output, "event: response.created\n")
	assert.Contains(t, output, "event: response.in_progress\n")
	assert.Contains(t, output, "data:")
	assert.Contains(t, output, "\"type\":\"response.created\"")
	assert.Contains(t, output, "\n\n")
	assert.True(t, strings.Contains(output, "data: "))
}

func TestStreamConverter_ErrorHandling_NilWriter(t *testing.T) {
	t.Parallel()

	converter := &StreamConverter{
		responseID:  "test",
		outputItems: []OutputItem{},
		eventWriter: nil,
	}

	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")

	err := converter.ProcessChunk(chunk)
	assert.Error(t, err)
}

func TestStreamConverter_ProcessChunk_NoDoubleCompletion(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	converter := NewStreamConverter(buf)

	chunk1 := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
	err := converter.ProcessChunk(chunk1)
	require.NoError(t, err)

	chunk2 := []byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
	err = converter.ProcessChunk(chunk2)
	require.NoError(t, err)

	output := buf.String()
	count := strings.Count(output, "event: response.completed")
	assert.Equal(t, 1, count, "should only emit one response.completed event")

	buf.Reset()

	doneChunk := []byte("data: [DONE]\n\n")
	err = converter.ProcessChunk(doneChunk)
	require.NoError(t, err)

	assert.Empty(t, buf.String(), "[DONE] should be ignored after completion")
}
