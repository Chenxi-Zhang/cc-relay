package proxy

import (
	"strings"
	"testing"
)

// sseFixture mirrors the event sequence observed from the real Codex
// backend (base64 payloads shortened).
const sseFixture = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"},"sequence_number":0}

event: response.in_progress
data: {"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"},"sequence_number":1}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"ig_1","type":"image_generation_call","status":"in_progress"},"output_index":0,"sequence_number":2}

event: response.image_generation_call.partial_image
data: {"type":"response.image_generation_call.partial_image","item_id":"ig_1","partial_image_b64":"cGFydGlhbA==","partial_image_index":0,"sequence_number":5}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"completed","result":"ZmluYWwtaW1hZ2UtYjY0","revised_prompt":"a flat minimal red coffee mug icon","size":"1254x1254"},"output_index":0,"sequence_number":6}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}},"sequence_number":12}

`

func TestAggregateCodexImageSSEHappyPath(t *testing.T) {
	results, usage, err := aggregateCodexImageSSE(strings.NewReader(sseFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	r := results[0]
	if r.B64JSON != "ZmluYWwtaW1hZ2UtYjY0" {
		t.Errorf("b64 = %q", r.B64JSON)
	}
	if r.RevisedPrompt != "a flat minimal red coffee mug icon" {
		t.Errorf("revised_prompt = %q", r.RevisedPrompt)
	}
	if usage == nil || usage.TotalTokens != 46 || usage.InputTokens != 12 || usage.OutputTokens != 34 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestAggregateCodexImageSSEFailure(t *testing.T) {
	stream := `event: response.failed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"rate_limited","message":"quota exceeded"}},"sequence_number":3}

`
	_, _, err := aggregateCodexImageSSE(strings.NewReader(stream))
	if err == nil {
		t.Fatal("expected error for response.failed")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("error should surface upstream message, got: %v", err)
	}
}

func TestAggregateCodexImageSSEErrorEvent(t *testing.T) {
	stream := `event: error
data: {"type":"error","code":"server_error"}

`
	if _, _, err := aggregateCodexImageSSE(strings.NewReader(stream)); err == nil {
		t.Fatal("expected error for error event")
	}
}

func TestAggregateCodexImageSSETruncatedWithImage(t *testing.T) {
	stream := `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"YWJj"},"output_index":0}

`
	results, _, err := aggregateCodexImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("truncated stream with an image should be tolerated: %v", err)
	}
	if len(results) != 1 || results[0].B64JSON != "YWJj" {
		t.Errorf("results = %+v", results)
	}
}

func TestAggregateCodexImageSSEEmptyStream(t *testing.T) {
	if _, _, err := aggregateCodexImageSSE(strings.NewReader("")); err == nil {
		t.Fatal("expected error for stream without images")
	}
}

func TestAggregateCodexImageSSELargeLine(t *testing.T) {
	// A 1MB+ base64 payload must survive the line reader.
	big := strings.Repeat("A", 1_100_000)
	stream := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"" + big + "\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	results, _, err := aggregateCodexImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || len(results[0].B64JSON) != 1_100_000 {
		t.Fatalf("large payload lost: %d results", len(results))
	}
}

func TestAggregateCodexImageSSEIgnoresOtherItems(t *testing.T) {
	stream := `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":""}]},"output_index":1}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed"}}

`
	results, _, err := aggregateCodexImageSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("message items must be ignored, got %+v", results)
	}
}
