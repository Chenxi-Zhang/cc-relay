package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// SSE event types for Responses API streaming.
// These match the official Codex client expectations defined in
// codex-rs/codex-api/src/sse/responses.rs and codex-rs/core/tests/common/responses.rs.
const (
	EventResponseCreated            = "response.created"
	EventResponseInProgress         = "response.in_progress"
	EventOutputItemAdded            = "response.output_item.added"
	EventOutputTextDelta            = "response.output_text.delta"
	EventOutputTextDone             = "response.output_text.done"
	EventOutputItemDone             = "response.output_item.done"
	EventResponseCompleted          = "response.completed"
	EventFunctionCallArgumentsDelta = "response.function_call_arguments.delta"
)

// StreamConverter handles conversion from Chat Completions SSE chunks to Responses API SSE events
type StreamConverter struct {
	responseID    string
	outputItems   []OutputItem
	currentItem   *OutputItem
	contentBuffer string
	eventWriter   io.Writer
	completed     bool
	created       bool
	model         string
}

// NewStreamConverter creates a new stream converter for Responses API SSE events
func NewStreamConverter(eventWriter io.Writer) *StreamConverter {
	return &StreamConverter{
		responseID:  GenerateResponseID(),
		outputItems: make([]OutputItem, 0),
		eventWriter: eventWriter,
	}
}

// ProcessChunk processes a Chat Completions SSE chunk and emits corresponding Responses API events
func (sc *StreamConverter) ProcessChunk(chunkData []byte) error {
	if sc.completed {
		return nil // already completed, ignore further chunks
	}

	// Parse SSE chunk - Chat Completions format: "data: {json}\n\n"
	if len(chunkData) < 7 || !bytes.HasPrefix(chunkData, []byte("data: ")) {
		return fmt.Errorf("invalid SSE chunk format")
	}

	jsonData := chunkData[6 : len(chunkData)-2]

	// Handle [DONE] sentinel
	if string(jsonData) == "[DONE]" {
		return sc.emitCompletedEvent()
	}

	// Parse Chat Completions chunk
	var chunk map[string]any
	if err := json.Unmarshal(jsonData, &chunk); err != nil {
		return fmt.Errorf("failed to parse SSE chunk: %w", err)
	}

	// Capture model from first chunk
	if sc.model == "" {
		if m, ok := chunk["model"].(string); ok {
			sc.model = m
		}
	}

	if !sc.created {
		if err := sc.emitCreatedAndInProgressEvents(); err != nil {
			return err
		}
		sc.created = true
	}

	// Process content deltas
	if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				// Handle content delta
				content, _ := delta["content"].(string)
				if content != "" {
					if err := sc.emitContentDelta(content); err != nil {
						return err
					}
				}

				// Handle tool_calls in delta
				if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) > 0 {
					if err := sc.handleToolCallsDelta(toolCalls); err != nil {
						return err
					}
				}
			}

			// Handle finish reason
			if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
				if err := sc.emitFinishEvents(finishReason); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (sc *StreamConverter) emitCreatedAndInProgressEvents() error {
	resp := ResponsesAPIResponse{
		ID:        sc.responseID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     sc.model,
		Status:    "in_progress",
		Output:    []OutputItem{},
		Usage:     ResponseUsage{},
	}

	if err := sc.emitSSEEvent(EventResponseCreated, map[string]any{
		"type":     EventResponseCreated,
		"response": resp,
	}); err != nil {
		return err
	}

	return sc.emitSSEEvent(EventResponseInProgress, map[string]any{
		"type":     EventResponseInProgress,
		"response": resp,
	})
}

func (sc *StreamConverter) emitContentDelta(content string) error {
	if sc.currentItem == nil {
		itemID := GenerateItemID("msg")
		sc.currentItem = &OutputItem{
			ID:   itemID,
			Type: "message",
			Message: &OutputMessage{
				Role:    "assistant",
				Content: []ContentPart{{Type: "output_text", Text: ""}},
			},
		}
		sc.outputItems = append(sc.outputItems, *sc.currentItem)

		if err := sc.emitSSEEvent(EventOutputItemAdded, map[string]any{
			"type": EventOutputItemAdded,
			"item": map[string]any{
				"type":    "message",
				"role":    "assistant",
				"id":      itemID,
				"content": []ContentPart{{Type: "output_text", Text: ""}},
			},
		}); err != nil {
			return err
		}
	}

	sc.contentBuffer += content

	return sc.emitSSEEvent(EventOutputTextDelta, map[string]any{
		"type":  EventOutputTextDelta,
		"delta": content,
	})
}

func (sc *StreamConverter) handleToolCallsDelta(toolCalls []any) error {
	for _, tcRaw := range toolCalls {
		tc, ok := tcRaw.(map[string]any)
		if !ok {
			continue
		}

		callID, _ := tc["id"].(string)

		if callID != "" {
			funcItem := OutputItem{
				ID:   callID,
				Type: "function_call",
				FunctionCall: &FunctionCall{
					CallID: callID,
				},
			}

			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					funcItem.FunctionCall.Name = name
				}
			}

			sc.outputItems = append(sc.outputItems, funcItem)

			if err := sc.emitSSEEvent(EventOutputItemAdded, map[string]any{
				"type": EventOutputItemAdded,
				"item": map[string]any{
					"type":      "function_call",
					"id":        callID,
					"call_id":   callID,
					"name":      funcItem.FunctionCall.Name,
					"arguments": "",
				},
			}); err != nil {
				return err
			}
		}

		// Emit function_call_arguments.delta
		argsDelta := ""
		if fn, ok := tc["function"].(map[string]any); ok {
			if args, ok := fn["arguments"].(string); ok {
				argsDelta = args
			}
		}

		if argsDelta != "" {
			if err := sc.emitSSEEvent(EventFunctionCallArgumentsDelta, map[string]any{
				"type":    EventFunctionCallArgumentsDelta,
				"item_id": callID,
				"delta":   argsDelta,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (sc *StreamConverter) emitFinishEvents(finishReason string) error {
	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}

	if sc.currentItem != nil {
		if err := sc.emitSSEEvent(EventOutputTextDone, map[string]any{
			"type": EventOutputTextDone,
			"text": sc.contentBuffer,
		}); err != nil {
			return err
		}

		if err := sc.emitSSEEvent(EventOutputItemDone, map[string]any{
			"type": EventOutputItemDone,
			"item": map[string]any{
				"type":    "message",
				"role":    "assistant",
				"id":      sc.currentItem.ID,
				"content": []ContentPart{{Type: "output_text", Text: sc.contentBuffer}},
				"status":  "completed",
			},
		}); err != nil {
			return err
		}
	}

	for i := range sc.outputItems {
		item := &sc.outputItems[i]
		if item.Type == "function_call" {
			if err := sc.emitSSEEvent(EventOutputItemDone, map[string]any{
				"type": EventOutputItemDone,
				"item": map[string]any{
					"type":      "function_call",
					"id":        item.ID,
					"call_id":   item.FunctionCall.CallID,
					"name":      item.FunctionCall.Name,
					"arguments": item.FunctionCall.Arguments,
					"status":    "completed",
				},
			}); err != nil {
				return err
			}
		}
	}

	completedEvent := map[string]any{
		"type": EventResponseCompleted,
		"response": ResponsesAPIResponse{
			ID:        sc.responseID,
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Model:     sc.model,
			Status:    status,
			Output:    sc.outputItems,
			Usage:     ResponseUsage{},
		},
	}

	sc.completed = true
	return sc.emitSSEEvent(EventResponseCompleted, completedEvent)
}

func (sc *StreamConverter) emitCompletedEvent() error {
	if sc.completed {
		return nil
	}

	status := "completed"

	if sc.currentItem != nil {
		if err := sc.emitSSEEvent(EventOutputTextDone, map[string]any{
			"type": EventOutputTextDone,
			"text": sc.contentBuffer,
		}); err != nil {
			return err
		}

		if err := sc.emitSSEEvent(EventOutputItemDone, map[string]any{
			"type": EventOutputItemDone,
			"item": map[string]any{
				"type":    "message",
				"role":    "assistant",
				"id":      sc.currentItem.ID,
				"content": []ContentPart{{Type: "output_text", Text: sc.contentBuffer}},
				"status":  "completed",
			},
		}); err != nil {
			return err
		}
	}

	completedEvent := map[string]any{
		"type": EventResponseCompleted,
		"response": ResponsesAPIResponse{
			ID:        sc.responseID,
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Model:     sc.model,
			Status:    status,
			Output:    sc.outputItems,
			Usage:     ResponseUsage{},
		},
	}

	sc.completed = true
	return sc.emitSSEEvent(EventResponseCompleted, completedEvent)
}

// emitSSEEvent writes a properly formatted SSE event
func (sc *StreamConverter) emitSSEEvent(eventType string, data interface{}) error {
	if sc.eventWriter == nil {
		return fmt.Errorf("stream converter: event writer is nil")
	}

	// Write event type header
	if _, err := fmt.Fprintf(sc.eventWriter, "event: %s\n", eventType); err != nil {
		return err
	}

	// Marshal data as JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// Write data
	if _, err := fmt.Fprintf(sc.eventWriter, "data: %s\n\n", string(jsonData)); err != nil {
		return err
	}

	return nil
}

// Reset resets the converter state for a new stream
func (sc *StreamConverter) Reset() {
	sc.responseID = GenerateResponseID()
	sc.outputItems = make([]OutputItem, 0)
	sc.currentItem = nil
	sc.contentBuffer = ""
	sc.completed = false
	sc.created = false
	sc.model = ""
}
