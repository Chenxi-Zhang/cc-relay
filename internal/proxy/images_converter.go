package proxy

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// codexImageContentPart is one content part of a user input message.
type codexImageContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// codexImageInputItem is one input item of a Codex Responses request.
type codexImageInputItem struct {
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []codexImageContentPart `json:"content"`
}

// codexImageTool is the hosted image_generation tool definition.
type codexImageTool struct {
	Type              string `json:"type"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
}

// codexImageReasoning controls host-model reasoning effort.
type codexImageReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

// codexImageRequest is the outbound Codex backend Responses request.
type codexImageRequest struct {
	Model             string                `json:"model"`
	Instructions      string                `json:"instructions"`
	Input             []codexImageInputItem `json:"input"`
	Tools             []codexImageTool      `json:"tools"`
	ToolChoice        string                `json:"tool_choice"`
	ParallelToolCalls bool                  `json:"parallel_tool_calls"`
	PromptCacheKey    string                `json:"prompt_cache_key,omitempty"`
	Reasoning         *codexImageReasoning  `json:"reasoning"`
	Store             bool                  `json:"store"`
	Stream            bool                  `json:"stream"`
}

// BuildCodexImageRequest translates an Images API request into a Codex
// backend Responses request that triggers the hosted image_generation tool.
// The prompt becomes an input_text part; image parts are attached separately
// by the edits endpoint via prependImageParts.
func BuildCodexImageRequest(req *ImagesGenerateRequest, hostModel, instructions string) (*codexImageRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("images converter: request is nil")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	tool := codexImageTool{Type: "image_generation"}
	if req.Size != "" {
		tool.Size = req.Size
	}
	if req.Quality != "" {
		tool.Quality = req.Quality
	}
	if req.Background != "" {
		tool.Background = req.Background
	}
	if req.Moderation != "" {
		tool.Moderation = req.Moderation
	}
	if req.OutputFormat != "" {
		tool.OutputFormat = req.OutputFormat
	}
	if req.OutputCompression != nil {
		tool.OutputCompression = req.OutputCompression
	}

	return &codexImageRequest{
		Model:        hostModel,
		Instructions: instructions,
		Input: []codexImageInputItem{{
			Type:    "message",
			Role:    "user",
			Content: []codexImageContentPart{{Type: "input_text", Text: req.Prompt}},
		}},
		Tools:             []codexImageTool{tool},
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Reasoning:         &codexImageReasoning{Effort: "low", Summary: "auto"},
		Store:             false,
		Stream:            true,
	}, nil
}

// prependImageParts adds input_image content parts (data URLs) in front of
// the prompt text part. It is used by the edits endpoint where uploaded
// file contents must be shown to the model alongside the prompt.
func prependImageParts(req *codexImageRequest, payloads []imagePayload) error {
	if len(req.Input) != 1 || len(req.Input[0].Content) != 1 {
		return fmt.Errorf("images converter: unexpected input structure")
	}
	textPart := req.Input[0].Content[0]

	parts := make([]codexImageContentPart, 0, len(payloads)+1)
	for _, p := range payloads {
		parts = append(parts, codexImageContentPart{
			Type:     "input_image",
			ImageURL: imageDataURL(p.MimeType, p.Data),
		})
	}
	req.Input[0].Content = append(parts, textPart)
	return nil
}

// imagePayload is one uploaded image from an edits request.
type imagePayload struct {
	MimeType string
	Data     []byte
}

// imageDataURL builds a base64 data URL for an image payload.
func imageDataURL(mime string, payload []byte) string {
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(payload)
}
