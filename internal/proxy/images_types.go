package proxy

import "time"

// ImagesGenerateRequest is the OpenAI Images API request accepted by
// POST /v1/images/generations and POST /v1/images/edits.
// See https://platform.openai.com/docs/api-reference/images.
type ImagesGenerateRequest struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	N               int    `json:"n,omitempty"`
	Size            string `json:"size,omitempty"`
	Quality         string `json:"quality,omitempty"`
	Style           string `json:"style,omitempty"`
	Background      string `json:"background,omitempty"`
	Moderation      string `json:"moderation,omitempty"`
	OutputFormat    string `json:"output_format,omitempty"`
	OutputCompression *int `json:"output_compression,omitempty"`
	ResponseFormat  string `json:"response_format,omitempty"`
	User            string `json:"user,omitempty"`
}

// ImagesResponseData is one generated image in an ImagesResponse.
type ImagesResponseData struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImagesUsage reports token usage reported by the Codex backend.
type ImagesUsage struct {
	TotalTokens  int `json:"total_tokens"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ImagesResponse is the OpenAI Images API response format.
type ImagesResponse struct {
	Created int64                `json:"created"`
	Data    []ImagesResponseData `json:"data"`
	Usage   *ImagesUsage         `json:"usage,omitempty"`
}

// newImagesResponse builds a response with the current timestamp.
func newImagesResponse(data []ImagesResponseData, usage *ImagesUsage) ImagesResponse {
	return ImagesResponse{
		Created: time.Now().Unix(),
		Data:    data,
		Usage:   usage,
	}
}

// imagesErrorDetail is the OpenAI-style error envelope.
type imagesErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// imagesErrorResponse is the OpenAI-style error response body.
type imagesErrorResponse struct {
	Error imagesErrorDetail `json:"error"`
}
