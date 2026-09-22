package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCodexImageRequestPassthrough(t *testing.T) {
	compression := 90
	req := &ImagesGenerateRequest{
		Prompt:           "a red coffee mug icon",
		Size:             "1024x1024",
		Quality:          "low",
		Background:       "transparent",
		OutputFormat:     "png",
		OutputCompression: &compression,
	}

	out, err := BuildCodexImageRequest(req, "gpt-test", "instructions here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", out.Model)
	}
	if out.Instructions != "instructions here" {
		t.Errorf("instructions = %q", out.Instructions)
	}
	if len(out.Input) != 1 || out.Input[0].Role != "user" {
		t.Fatalf("input structure wrong: %+v", out.Input)
	}
	if len(out.Input[0].Content) != 1 || out.Input[0].Content[0].Type != "input_text" {
		t.Fatalf("content structure wrong: %+v", out.Input[0].Content)
	}
	if got := out.Input[0].Content[0].Text; got != "a red coffee mug icon" {
		t.Errorf("prompt text = %q", got)
	}

	if len(out.Tools) != 1 {
		t.Fatalf("tools = %+v", out.Tools)
	}
	tool := out.Tools[0]
	if tool.Type != "image_generation" {
		t.Errorf("tool type = %q", tool.Type)
	}
	if tool.Size != "1024x1024" || tool.Quality != "low" || tool.Background != "transparent" {
		t.Errorf("tool params not passed through: %+v", tool)
	}
	if tool.OutputFormat != "png" || tool.OutputCompression == nil || *tool.OutputCompression != 90 {
		t.Errorf("output format/compression wrong: %+v", tool)
	}

	if !out.Stream || out.Store {
		t.Errorf("stream=%v store=%v, want stream=true store=false", out.Stream, out.Store)
	}

	// Sanity: the outbound JSON marshals and the tool type survives.
	var raw map[string]interface{}
	if err := json.Unmarshal(mustJSON(out), &raw); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tools := raw["tools"].([]interface{})
	toolMap := tools[0].(map[string]interface{})
	if toolMap["type"] != "image_generation" {
		t.Errorf("json tool type = %v", toolMap["type"])
	}
}

func TestBuildCodexImageRequestEmptyToolParams(t *testing.T) {
	out, err := BuildCodexImageRequest(&ImagesGenerateRequest{Prompt: "hi"}, "m", "i")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tool := out.Tools[0]
	if tool.Size != "" || tool.Quality != "" || tool.OutputFormat != "" {
		t.Errorf("empty params should stay empty: %+v", tool)
	}
}

func TestBuildCodexImageRequestPromptRequired(t *testing.T) {
	if _, err := BuildCodexImageRequest(&ImagesGenerateRequest{}, "m", "i"); err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if _, err := BuildCodexImageRequest(nil, "m", "i"); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestPrependImagePartsOrder(t *testing.T) {
	out, err := BuildCodexImageRequest(&ImagesGenerateRequest{Prompt: "make it blue"}, "m", "i")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	png := []byte{0x89, 0x50, 0x4E, 0x47}
	err = prependImageParts(out, []imagePayload{
		{MimeType: "image/png", Data: png},
		{MimeType: "", Data: png}, // falls back to image/png
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := out.Input[0].Content
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if parts[0].Type != "input_image" || parts[1].Type != "input_image" {
		t.Errorf("first parts must be images: %+v", parts[:2])
	}
	if parts[2].Type != "input_text" || parts[2].Text != "make it blue" {
		t.Errorf("last part must be the prompt: %+v", parts[2])
	}
	if !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Errorf("image url = %q", parts[0].ImageURL)
	}
	if !strings.HasPrefix(parts[1].ImageURL, "data:image/png;base64,") {
		t.Errorf("fallback mime wrong: %q", parts[1].ImageURL)
	}
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return bytes.TrimSpace(b)
}
