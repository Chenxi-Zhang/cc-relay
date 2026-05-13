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

	chatReq := &ChatCompletionRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Tools:       req.Tools,
	}

	// Map stream flag — pointer in Responses API, value in Chat Completions
	if req.Stream != nil {
		chatReq.Stream = *req.Stream
	}

	// Map max_output_tokens → max_tokens
	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Convert input items to chat messages
	messages := make([]ChatMessage, 0, len(req.Input)+1)

	// Convert instructions → system message (prepend)
	if req.Instructions != "" {
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	for _, item := range req.Input {
		msg, ok := convertInputItem(item)
		if !ok {
			continue // skip unknown item types
		}
		messages = append(messages, msg)
	}
	chatReq.Messages = messages

	return chatReq, nil
}

// convertInputItem converts a single Responses API input item to a ChatMessage.
// Returns the message and true on success, or a zero ChatMessage and false for
// unrecognized item types (which are silently skipped).
func convertInputItem(item InputItem) (ChatMessage, bool) {
	switch item.Type {
	case "message":
		if item.Message == nil {
			return ChatMessage{}, false
		}
		return ChatMessage{
			Role:    item.Message.Role,
			Content: item.Message.Content.Text(),
		}, true

	case "function_call_output":
		if item.FunctionCallOutput == nil {
			return ChatMessage{}, false
		}
		return ChatMessage{
			Role:       "tool",
			Content:    item.FunctionCallOutput.Output,
			ToolCallID: item.FunctionCallOutput.CallID,
		}, true

	default:
		return ChatMessage{}, false
	}
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
