package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// codexImageResult is one aggregated image from a Codex SSE stream.
type codexImageResult struct {
	B64JSON       string
	RevisedPrompt string
}

// codexUsage is the token usage reported in response.completed.
type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// SSE JSON event shapes emitted by the Codex backend that we care about.
type codexSSEEvent struct {
	Type     string `json:"type"`
	Item     *codexSSEItem `json:"item,omitempty"`
	Response *codexSSEResponse `json:"response,omitempty"`
}

type codexSSEItem struct {
	Type          string `json:"type"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	RevisedPrompt string `json:"revised_prompt"`
}

type codexSSEResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Usage  *codexUsage `json:"usage"`
	Error  *codexSSEError `json:"error"`
}

type codexSSEError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// maxSSELineBytes caps a single SSE line. Base64 image payloads routinely
// exceed 1MB, so this must be far above bufio's default 4KB token size.
const maxSSELineBytes = 64 << 20 // 64 MiB

// aggregateCodexImageSSE reads a Codex backend SSE stream and collects the
// completed image_generation_call results. It returns an error when the
// stream reports response.failed, an error event, or ends without a
// terminal event.
func aggregateCodexImageSSE(r io.Reader) ([]codexImageResult, *codexUsage, error) {
	reader := bufio.NewReaderSize(r, 64*1024)

	var results []codexImageResult
	var usage *codexUsage
	completed := false

	for {
		line, err := readLongLine(reader)
		if len(line) > 0 {
			if data, ok := parseSSEDataLine(line); ok {
				var ev codexSSEEvent
				if jsonErr := json.Unmarshal(data, &ev); jsonErr == nil {
					switch ev.Type {
					case "response.output_item.done":
						collectImageItem(&results, ev.Item)
					case "response.completed":
						if ev.Response != nil {
							usage = ev.Response.Usage
						}
						completed = true
					case "response.failed":
						return nil, nil, sseFailureError(ev)
					case "error":
						return nil, nil, sseFailureError(ev)
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if !completed {
					if len(results) == 0 {
						return nil, nil, fmt.Errorf("codex image stream ended without a completed response")
					}
					// Tolerate a truncated stream that still delivered images.
					return results, usage, nil
				}
				return results, usage, nil
			}
			return nil, nil, fmt.Errorf("reading codex image stream: %w", err)
		}
	}
}

// readLongLine reads one line without bufio.Scanner's token size ceiling.
func readLongLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err == nil {
			return trimEOL(buf), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(buf) > maxSSELineBytes {
				return nil, fmt.Errorf("codex image stream line exceeds %d bytes", maxSSELineBytes)
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return trimEOL(buf), io.EOF
		}
		return nil, err
	}
}

// trimEOL strips trailing CR/LF.
func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// parseSSEDataLine returns the JSON payload of a "data: " line.
func parseSSEDataLine(line []byte) ([]byte, bool) {
	s := string(line)
	if !strings.HasPrefix(s, "data:") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	if payload == "" || payload == "[DONE]" {
		return nil, false
	}
	return []byte(payload), true
}

// collectImageItem appends an image_generation_call result.
func collectImageItem(results *[]codexImageResult, item *codexSSEItem) {
	if item == nil || item.Type != "image_generation_call" || item.Result == "" {
		return
	}
	*results = append(*results, codexImageResult{
		B64JSON:       item.Result,
		RevisedPrompt: item.RevisedPrompt,
	})
}

// sseFailureError converts a failed/error event into a descriptive error.
func sseFailureError(ev codexSSEEvent) error {
	if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
		return fmt.Errorf("codex image generation failed: %s", ev.Response.Error.Message)
	}
	if ev.Item != nil && ev.Item.Status == "failed" {
		return fmt.Errorf("codex image generation failed for item")
	}
	return fmt.Errorf("codex image generation failed (event %s)", ev.Type)
}
