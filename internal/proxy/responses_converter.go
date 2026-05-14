package proxy

import (
	"errors"
	"time"
)

// ConvertRequest converts a Responses API request to a Chat Completions request.
// This is the core translation that enables Codex CLI to work with third-party
// providers via the standard OpenAI Chat Completions format.
func ConvertRequest(req *ResponsesAPIRequest) (*ChatCompletionRequest, error) {
	if req == nil {
		return nil, errors.New("responses converter: request is nil")
	}

	// Filter tools: only "function" type is supported by Chat Completions API.
	// Responses API supports additional tool types (web_search, code_interpreter,
	// file_search, mcp, etc.) that are OpenAI-hosted and have no third-party equivalent.
	chatReq := &ChatCompletionRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Tools:       filterFunctionTools(req.Tools),
	}

	// Map stream flag — pointer in Responses API, value in Chat Completions
	if req.Stream != nil {
		chatReq.Stream = *req.Stream
	}

	// Map max_output_tokens → max_tokens
	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Convert input items to chat messages.
	// Consecutive function_call items are grouped into a single assistant message
	// with tool_calls, matching Chat Completions format requirements.
	messages := make([]ChatMessage, 0, len(req.Input)+1)

	if req.Instructions != "" {
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	var pendingToolCalls []ToolCall

	for _, item := range req.Input {
		switch item.Type {
		case "message":
			messages, pendingToolCalls = flushToolCalls(messages, pendingToolCalls)
			if item.Message == nil || item.Message.Role == "" {
				continue
			}
			messages = append(messages, ChatMessage{
				Role:    item.Message.Role,
				Content: item.Message.Content.Text(),
			})

		case "function_call":
			if item.FunctionCallInput != nil {
				pendingToolCalls = append(pendingToolCalls, ToolCall{
					ID:   item.FunctionCallInput.CallID,
					Type: "function",
					Function: ToolCallFunction{
						Name:      item.FunctionCallInput.Name,
						Arguments: item.FunctionCallInput.Arguments,
					},
				})
			}

		case "function_call_output":
			messages, pendingToolCalls = flushToolCalls(messages, pendingToolCalls)
			if item.FunctionCallOutput == nil {
				continue
			}
			messages = append(messages, ChatMessage{
				Role:       "tool",
				Content:    item.FunctionCallOutput.Output,
				ToolCallID: item.FunctionCallOutput.CallID,
			})
		}
	}
	messages, _ = flushToolCalls(messages, pendingToolCalls)
	chatReq.Messages = messages

	return chatReq, nil
}

// flushToolCalls emits pending function_call items as a single assistant ChatMessage
// with tool_calls, then clears the pending list. This ensures the Chat Completions
// requirement that tool result messages (role: "tool") always follow an assistant
// message with matching tool_calls.
func flushToolCalls(messages []ChatMessage, pending []ToolCall) ([]ChatMessage, []ToolCall) {
	if len(pending) == 0 {
		return messages, nil
	}
	messages = append(messages, ChatMessage{
		Role:      "assistant",
		Content:   "",
		ToolCalls: pending,
	})
	return messages, nil
}

// ConvertResponse converts a Chat Completions response to a Responses API response.
// This is the reverse translation: taking the upstream provider's Chat Completions
// format and producing a Responses API response for clients like Codex CLI.
func ConvertResponse(chatResp *ChatCompletionResponse) (*ResponsesAPIResponse, error) {
	if chatResp == nil {
		return nil, errors.New("responses converter: response is nil")
	}

	if len(chatResp.Choices) == 0 {
		return nil, errors.New("responses converter: no choices in response")
	}

	choice := chatResp.Choices[0]

	// Build output items: always include a message item for the assistant response,
	// plus function_call items for each tool_call.
	output := buildOutputItems(choice.Message)

	// Map finish_reason → status
	status := mapFinishReason(choice.FinishReason)

	return &ResponsesAPIResponse{
		ID:        GenerateResponseID(),
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     chatResp.Model,
		Status:    status,
		Output:    output,
		Usage: ResponseUsage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:  chatResp.Usage.TotalTokens,
		},
	}, nil
}

// buildOutputItems creates OutputItem slice from a Chat Completions message.
// Always produces a message output item, plus function_call items for any tool_calls.
func buildOutputItems(msg Message) []OutputItem {
	items := make([]OutputItem, 0, 1+len(msg.ToolCalls))

	// Message output item — always present
	content := []ContentPart{}
	if msg.Content != "" {
		content = append(content, ContentPart{
			Type: "output_text",
			Text: msg.Content,
		})
	}
	items = append(items, OutputItem{
		ID:   GenerateItemID("msg"),
		Type: "message",
		Message: &OutputMessage{
			Role:    msg.Role,
			Content: content,
		},
	})

	// Function call output items
	for _, tc := range msg.ToolCalls {
		items = append(items, OutputItem{
			ID:   tc.ID,
			Type: "function_call",
			FunctionCall: &FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
				CallID:    tc.ID,
			},
		})
	}

	return items
}

// filterFunctionTools strips tool types unsupported by Chat Completions API.
// Only "function" tools pass through; hosted tools (web_search, code_interpreter,
// file_search, mcp, image_generation, etc.) are dropped silently.
func filterFunctionTools(tools []Tool) []Tool {
	if len(tools) == 0 {
		return tools
	}
	filtered := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// mapFinishReason converts a Chat Completions finish_reason to a Responses API status.
func mapFinishReason(reason *string) string {
	if reason == nil {
		return "completed"
	}
	switch *reason {
	case "stop", "tool_calls":
		return "completed"
	default:
		// "length", "content_filter", and any unknown reasons
		return "incomplete"
	}
}
