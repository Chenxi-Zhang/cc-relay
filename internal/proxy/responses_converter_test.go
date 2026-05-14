package proxy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertResponse_SimpleTextResponse(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	
	chatResp := &ChatCompletionResponse{
		ID:      "chat_123",
		Object:  "chat.completion",
		Created: now,
		Model:   "gpt-3.5-turbo",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "Hello! How can I help you today?",
				},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{
			PromptTokens:  5,
			CompletionTokens: 10,
			TotalTokens:   15,
		},
	}

	response, err := ConvertResponse(chatResp)

	require.NoError(t, err)
	require.NotNil(t, response)
	
	// Verify basic fields
	assert.NotEmpty(t, response.ID)
	assert.Equal(t, "response", response.Object)
	assert.GreaterOrEqual(t, response.CreatedAt, now) // Allow slight time differences
	assert.Equal(t, "gpt-3.5-turbo", response.Model)
	assert.Equal(t, "completed", response.Status)
	
	// Verify output items
	require.Len(t, response.Output, 1)
	assert.Equal(t, "message", response.Output[0].Type)
	require.NotNil(t, response.Output[0].Message)
	assert.NotEmpty(t, response.Output[0].ID)
	assert.Equal(t, "assistant", response.Output[0].Message.Role)
	assert.Equal(t, []ContentPart{{Type: "output_text", Text: "Hello! How can I help you today?"}}, response.Output[0].Message.Content)
	assert.Equal(t, 5, response.Usage.InputTokens)
	assert.Equal(t, 10, response.Usage.OutputTokens)
	assert.Equal(t, 15, response.Usage.TotalTokens)
}

func TestConvertResponse_WithToolCalls(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		ID:      "chat_456",
		Object:  "chat.completion",
		Model:   "gpt-4",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role: "assistant",
					Content: "I'll help you get the weather information.",
					ToolCalls: []ToolCall{
						{
							ID: "call_789",
							Function: ToolCallFunction{
								Name:      "get_weather",
								Arguments: `{"location": "New York"}`,
							},
						},
					},
				},
				FinishReason: pointerToString("tool_calls"),
			},
		},
		Usage: Usage{
			PromptTokens:  8,
			CompletionTokens: 12,
			TotalTokens:   20,
		},
	}

	response, err := ConvertResponse(chatResp)

	require.NoError(t, err)
	require.NotNil(t, response)
	
	assert.Equal(t, "completed", response.Status)
	
	// Verify output items - should have both message and function_call
	require.Len(t, response.Output, 2)
	
	// First item should be the message
	assert.Equal(t, "message", response.Output[0].Type)
	assert.NotEmpty(t, response.Output[0].ID)
	require.NotNil(t, response.Output[0].Message)
	assert.Equal(t, "assistant", response.Output[0].Message.Role)
	assert.Equal(t, []ContentPart{{Type: "output_text", Text: "I'll help you get the weather information."}}, response.Output[0].Message.Content)

	// Second item should be the function call
	assert.Equal(t, "function_call", response.Output[1].Type)
	assert.NotEmpty(t, response.Output[1].ID)
	require.NotNil(t, response.Output[1].FunctionCall)
	assert.Equal(t, "get_weather", response.Output[1].FunctionCall.Name)
	assert.Equal(t, `{"location": "New York"}`, response.Output[1].FunctionCall.Arguments)
	assert.Equal(t, "call_789", response.Output[1].FunctionCall.CallID)
}

func TestConvertResponse_FinishReasonMapping(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		finishReason *string
		expectedStatus string
	}{
		{
			name:         "stop reason",
			finishReason: pointerToString("stop"),
			expectedStatus: "completed",
		},
		{
			name:         "tool_calls reason",
			finishReason: pointerToString("tool_calls"),
			expectedStatus: "completed",
		},
		{
			name:         "length reason",
			finishReason: pointerToString("length"),
			expectedStatus: "incomplete",
		},
		{
			name:         "content_filter reason",
			finishReason: pointerToString("content_filter"),
			expectedStatus: "incomplete",
		},
		{
			name:         "unknown reason",
			finishReason: pointerToString("unknown"),
			expectedStatus: "incomplete",
		},
		{
			name:         "nil reason",
			finishReason: nil,
			expectedStatus: "completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			chatResp := &ChatCompletionResponse{
				ID:     "chat_test",
				Object: "chat.completion",
				Model:  "gpt-3.5-turbo",
				Choices: []Choice{
					{
						Index: 0,
						Message: Message{
							Role:    "assistant",
							Content: "Test response",
						},
						FinishReason: tc.finishReason,
					},
				},
				Usage: Usage{
					PromptTokens:     3,
					CompletionTokens: 5,
					TotalTokens:      8,
				},
			}

			response, err := ConvertResponse(chatResp)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedStatus, response.Status)
		})
	}
}

func TestConvertResponse_UsageMapping(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		ID:     "chat_usage",
		Object: "chat.completion",
		Model:  "gpt-4",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "This is a test response with usage tracking.",
				},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{
			PromptTokens:     25,
			CompletionTokens: 50,
			TotalTokens:      75,
		},
	}

	response, err := ConvertResponse(chatResp)

	require.NoError(t, err)
	
	assert.Equal(t, 25, response.Usage.InputTokens)
	assert.Equal(t, 50, response.Usage.OutputTokens)
	assert.Equal(t, 75, response.Usage.TotalTokens)
}

func TestConvertResponse_EmptyContent(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		ID:     "chat_empty",
		Object: "chat.completion",
		Model:  "gpt-3.5-turbo",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "",
				},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{
			PromptTokens:     1,
			CompletionTokens: 0,
			TotalTokens:      1,
		},
	}

	response, err := ConvertResponse(chatResp)

	require.NoError(t, err)
	require.NotNil(t, response)
	
	// Should still create a message output item, but with empty content array
	require.Len(t, response.Output, 1)
	assert.Equal(t, "message", response.Output[0].Type)
	require.NotNil(t, response.Output[0].Message)
	assert.Equal(t, "assistant", response.Output[0].Message.Role)
	assert.Empty(t, response.Output[0].Message.Content)
}

func TestConvertResponse_ErrorCases(t *testing.T) {
	t.Parallel()

	t.Run("nil response", func(t *testing.T) {
		response, err := ConvertResponse(nil)
		assert.Error(t, err)
		assert.Nil(t, response)
		assert.Contains(t, err.Error(), "response is nil")
	})

	t.Run("no choices", func(t *testing.T) {
		chatResp := &ChatCompletionResponse{
			ID:     "chat_no_choices",
			Object: "chat.completion",
			Model:  "gpt-3.5-turbo",
			Choices: []Choice{},
			Usage: Usage{
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
			},
		}

		response, err := ConvertResponse(chatResp)
		assert.Error(t, err)
		assert.Nil(t, response)
		assert.Contains(t, err.Error(), "no choices in response")
	})
}

// Helper function to create string pointer
func pointerToString(s string) *string {
	return &s
}

func TestConvertRequest_SimpleUserMessage(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{
				Type: "message",
				Message: &MessageInput{
					Role:    "user",
					Content: MessageContent{Raw: "Hello, how are you?", IsString: true},
				},
			},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "gpt-4o", result.Model)
	assert.False(t, result.Stream, "stream should default to false when nil")
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "user", result.Messages[0].Role)
	assert.Equal(t, "Hello, how are you?", result.Messages[0].Content)
}

func TestConvertRequest_MultiTurnConversation(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{
				Type: "message",
				Message: &MessageInput{
					Role:    "system",
					Content: MessageContent{Raw: "You are a helpful assistant.", IsString: true},
				},
			},
			{
				Type: "message",
				Message: &MessageInput{
					Role:    "user",
					Content: MessageContent{Raw: "What is 2+2?", IsString: true},
				},
			},
			{
				Type: "message",
				Message: &MessageInput{
					Role:    "assistant",
					Content: MessageContent{Raw: "2+2 equals 4.", IsString: true},
				},
			},
			{
				Type: "message",
				Message: &MessageInput{
					Role:    "user",
					Content: MessageContent{Raw: "And 3+3?", IsString: true},
				},
			},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "gpt-4o", result.Model)
	require.Len(t, result.Messages, 4)

	assert.Equal(t, "system", result.Messages[0].Role)
	assert.Equal(t, "You are a helpful assistant.", result.Messages[0].Content)
	assert.Equal(t, "user", result.Messages[1].Role)
	assert.Equal(t, "What is 2+2?", result.Messages[1].Content)
	assert.Equal(t, "assistant", result.Messages[2].Role)
	assert.Equal(t, "2+2 equals 4.", result.Messages[2].Content)
	assert.Equal(t, "user", result.Messages[3].Role)
	assert.Equal(t, "And 3+3?", result.Messages[3].Content)
}

func TestConvertRequest_WithStream(t *testing.T) {
	t.Parallel()

	streamTrue := true
	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{
				Type: "message",
				Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "hi", IsString: true}},
			},
		},
		Stream: &streamTrue,
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	assert.True(t, result.Stream)
}

func TestConvertRequest_WithOptionalFields(t *testing.T) {
	t.Parallel()

	temp := 0.7
	maxTokens := 1024
	topP := 0.9
	stream := false

	req := &ResponsesAPIRequest{
		Model:            "gpt-4o",
		Input:            []InputItem{},
		Stream:           &stream,
		Temperature:      &temp,
		MaxOutputTokens:  &maxTokens,
		TopP:             &topP,
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o", result.Model)
	assert.False(t, result.Stream)
	require.NotNil(t, result.Temperature)
	assert.InDelta(t, 0.7, *result.Temperature, 0.001)
	require.NotNil(t, result.MaxTokens)
	assert.Equal(t, 1024, *result.MaxTokens)
	require.NotNil(t, result.TopP)
	assert.InDelta(t, 0.9, *result.TopP, 0.001)
}

func TestConvertRequest_NilOptionalFields(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		// Stream, Temperature, MaxOutputTokens, TopP all nil
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "hi", IsString: true}}},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)

	assert.False(t, result.Stream, "stream should be false when nil")
	assert.Nil(t, result.Temperature)
	assert.Nil(t, result.MaxTokens)
	assert.Nil(t, result.TopP)
}

func TestConvertRequest_WithTools(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "What's the weather?", IsString: true}}},
		},
		Tools: []Tool{
			{
				Type: "function",
				Function: Function{
					Name:        "get_weather",
					Description: "Get the current weather",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)

	require.Len(t, result.Tools, 1)
	assert.Equal(t, "function", result.Tools[0].Type)
	assert.Equal(t, "get_weather", result.Tools[0].Function.Name)
	assert.Equal(t, "Get the current weather", result.Tools[0].Function.Description)
}

func TestConvertRequest_WithFunctionCallOutput(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "What's the weather in NYC?", IsString: true}}},
			{
				Type: "function_call",
				FunctionCallInput: &FunctionCallInput{
					ID:        "fc_001",
					CallID:    "call_abc123",
					Name:      "get_weather",
					Arguments: `{"city": "NYC"}`,
				},
			},
			{
				Type: "function_call_output",
				FunctionCallOutput: &FunctionCallOutput{
					CallID: "call_abc123",
					Output: `{"temperature": 72, "condition": "sunny"}`,
				},
			},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)

	// user → assistant(tool_calls) → tool
	require.Len(t, result.Messages, 3)
	assert.Equal(t, "user", result.Messages[0].Role)
	assert.Equal(t, "What's the weather in NYC?", result.Messages[0].Content)

	// function_call becomes assistant message with tool_calls
	assert.Equal(t, "assistant", result.Messages[1].Role)
	require.Len(t, result.Messages[1].ToolCalls, 1)
	assert.Equal(t, "call_abc123", result.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "get_weather", result.Messages[1].ToolCalls[0].Function.Name)
	assert.Equal(t, `{"city": "NYC"}`, result.Messages[1].ToolCalls[0].Function.Arguments)

	// function_call_output becomes tool role message
	assert.Equal(t, "tool", result.Messages[2].Role)
	assert.Equal(t, `{"temperature": 72, "condition": "sunny"}`, result.Messages[2].Content)
	assert.Equal(t, "call_abc123", result.Messages[2].ToolCallID)
}

func TestConvertRequest_MixedMessagesAndFunctionOutputs(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "system", Content: MessageContent{Raw: "You are a weather bot.", IsString: true}}},
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "Weather in Paris?", IsString: true}}},
			{
				Type: "function_call",
				FunctionCallInput: &FunctionCallInput{
					ID:        "fc_001",
					CallID:    "call_xyz",
					Name:      "get_weather",
					Arguments: `{"city": "Paris"}`,
				},
			},
			{
				Type: "function_call_output",
				FunctionCallOutput: &FunctionCallOutput{
					CallID: "call_xyz",
					Output: `{"temperature": 18, "condition": "cloudy"}`,
				},
			},
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "Thanks!", IsString: true}}},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)

	// system → user → assistant(tool_calls) → tool → user
	require.Len(t, result.Messages, 5)
	assert.Equal(t, "system", result.Messages[0].Role)
	assert.Equal(t, "user", result.Messages[1].Role)
	assert.Equal(t, "assistant", result.Messages[2].Role)
	require.Len(t, result.Messages[2].ToolCalls, 1)
	assert.Equal(t, "call_xyz", result.Messages[2].ToolCalls[0].ID)
	assert.Equal(t, "tool", result.Messages[3].Role)
	assert.Equal(t, `{"temperature": 18, "condition": "cloudy"}`, result.Messages[3].Content)
	assert.Equal(t, "user", result.Messages[4].Role)
}

func TestConvertRequest_EmptyInput(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Messages)
}

func TestConvertRequest_NilInput(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: nil,
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Messages)
}

func TestConvertRequest_EmptyTools(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "hi", IsString: true}}},
		},
		Tools: []Tool{},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	assert.Empty(t, result.Tools, "empty tools should produce empty tools in result")
}

func TestConvertRequest_NilRequest(t *testing.T) {
	t.Parallel()

	result, err := ConvertRequest(nil)
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestConvertRequest_TableDriven(t *testing.T) {
	t.Parallel()

	streamTrue := true
	streamFalse := false
	temp := 0.5
	maxTok := 500

	tests := []struct {
		name           string
		req            *ResponsesAPIRequest
		wantModel      string
		wantStream     bool
		wantMsgCount   int
		wantMsg0Role   string
		wantMsg0Content string
	}{
		{
			name: "single user message",
			req: &ResponsesAPIRequest{
				Model: "deepseek-chat",
				Input: []InputItem{
					{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "hello", IsString: true}}},
				},
			},
			wantModel:      "deepseek-chat",
			wantStream:     false,
			wantMsgCount:   1,
			wantMsg0Role:   "user",
			wantMsg0Content: "hello",
		},
		{
			name: "streaming enabled",
			req: &ResponsesAPIRequest{
				Model:  "deepseek-reasoner",
				Input:  []InputItem{{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "think", IsString: true}}}},
				Stream: &streamTrue,
			},
			wantModel:      "deepseek-reasoner",
			wantStream:     true,
			wantMsgCount:   1,
			wantMsg0Role:   "user",
			wantMsg0Content: "think",
		},
		{
			name: "with temperature and max_tokens",
			req: &ResponsesAPIRequest{
				Model:           "gpt-4o",
				Input:           []InputItem{{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "test", IsString: true}}}},
				Stream:          &streamFalse,
				Temperature:     &temp,
				MaxOutputTokens: &maxTok,
			},
			wantModel:      "gpt-4o",
			wantStream:     false,
			wantMsgCount:   1,
			wantMsg0Role:   "user",
			wantMsg0Content: "test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ConvertRequest(tc.req)
			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Equal(t, tc.wantModel, result.Model)
			assert.Equal(t, tc.wantStream, result.Stream)
			assert.Len(t, result.Messages, tc.wantMsgCount)
			if tc.wantMsgCount > 0 {
				assert.Equal(t, tc.wantMsg0Role, result.Messages[0].Role)
				assert.Equal(t, tc.wantMsg0Content, result.Messages[0].Content)
			}
		})
	}
}

// --- ConvertResponse tests ---


func TestConvertResponse_MultipleChoicesTakesFirst(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		Model: "gpt-4o",
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "First choice"},
				FinishReason: pointerToString("stop"),
			},
			{
				Index:        1,
				Message:      Message{Role: "assistant", Content: "Second choice"},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}

	result, err := ConvertResponse(chatResp)
	require.NoError(t, err)

	require.NotNil(t, result.Output[0].Message)
	assert.Equal(t, "First choice", result.Output[0].Message.Content[0].Text,
		"should use the first choice, not the second")
}

func TestConvertResponse_NilFinishReason(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		Model: "gpt-4o",
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "hello"},
				FinishReason: nil,
			},
		},
	}

	result, err := ConvertResponse(chatResp)
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status,
		"nil finish_reason should default to completed")
}

func TestConvertResponse_ToolCallsWithTextContent(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		Model: "gpt-4o",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "I'll check the weather for you.",
					ToolCalls: []ToolCall{
						{
							ID:   "call_weather",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "get_weather",
								Arguments: `{"location": "Paris"}`,
							},
						},
					},
				},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{PromptTokens: 15, CompletionTokens: 10, TotalTokens: 25},
	}

	result, err := ConvertResponse(chatResp)
	require.NoError(t, err)

	// Should have: 1 message + 1 function_call = 2 outputs
	require.Len(t, result.Output, 2)

	// First: message with text content
	assert.Equal(t, "message", result.Output[0].Type)
	assert.NotEmpty(t, result.Output[0].ID)
	require.NotNil(t, result.Output[0].Message)
	assert.Equal(t, "I'll check the weather for you.", result.Output[0].Message.Content[0].Text)

	// Second: function_call
	assert.Equal(t, "function_call", result.Output[1].Type)
	assert.NotEmpty(t, result.Output[1].ID)
	require.NotNil(t, result.Output[1].FunctionCall)
	assert.Equal(t, "get_weather", result.Output[1].FunctionCall.Name)
}

func TestConvertResponse_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	chatResp := &ChatCompletionResponse{
		Model: "gpt-4o",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: "",
					ToolCalls: []ToolCall{
						{
							ID:   "call_abc",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "get_weather",
								Arguments: `{"location": "NYC"}`,
							},
						},
						{
							ID:   "call_def",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "get_time",
								Arguments: `{"timezone": "EST"}`,
							},
						},
					},
				},
				FinishReason: pointerToString("stop"),
			},
		},
		Usage: Usage{PromptTokens: 20, CompletionTokens: 15, TotalTokens: 35},
	}

	result, err := ConvertResponse(chatResp)
	require.NoError(t, err)

	// 1 message + 2 function_calls = 3 outputs
	require.Len(t, result.Output, 3)

	// Second output: function_call for get_weather
	assert.Equal(t, "function_call", result.Output[1].Type)
	require.NotNil(t, result.Output[1].FunctionCall)
	assert.Equal(t, "get_weather", result.Output[1].FunctionCall.Name)
	assert.Equal(t, "call_abc", result.Output[1].FunctionCall.CallID)

	// Third output: function_call for get_time
	assert.Equal(t, "function_call", result.Output[2].Type)
	require.NotNil(t, result.Output[2].FunctionCall)
	assert.Equal(t, "get_time", result.Output[2].FunctionCall.Name)
	assert.Equal(t, "call_def", result.Output[2].FunctionCall.CallID)
}

func TestConvertRequest_FiltersNonFunctionTools(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "run code", IsString: true}}},
		},
		Tools: []Tool{
			{
				Type: "function",
				Function: Function{
					Name:        "shell",
					Description: "Run a shell command",
					Parameters:  map[string]any{"type": "object"},
				},
			},
			{
				Type: "web_search_preview",
			},
			{
				Type: "code_interpreter",
			},
			{
				Type: "file_search",
			},
			{
				Type: "function",
				Function: Function{
					Name:        "read_file",
					Description: "Read a file",
					Parameters:  map[string]any{"type": "object"},
				},
			},
			{
				Type: "mcp",
			},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, result.Tools, 2, "only function-type tools should pass through")
	assert.Equal(t, "function", result.Tools[0].Type)
	assert.Equal(t, "shell", result.Tools[0].Function.Name)
	assert.Equal(t, "function", result.Tools[1].Type)
	assert.Equal(t, "read_file", result.Tools[1].Function.Name)
}

func TestConvertRequest_AllNonFunctionTools_YieldsNil(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "search web", IsString: true}}},
		},
		Tools: []Tool{
			{Type: "web_search_preview"},
			{Type: "code_interpreter"},
			{Type: "file_search"},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Nil(t, result.Tools, "all-non-function tools should yield nil tools slice")
}

func TestConvertRequest_MultipleConsecutiveFunctionCalls(t *testing.T) {
	t.Parallel()

	req := &ResponsesAPIRequest{
		Model: "gpt-4o",
		Input: []InputItem{
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "Check weather and time", IsString: true}}},
			{
				Type: "function_call",
				FunctionCallInput: &FunctionCallInput{
					ID:        "fc_001",
					CallID:    "call_weather",
					Name:      "get_weather",
					Arguments: `{"city": "NYC"}`,
				},
			},
			{
				Type: "function_call",
				FunctionCallInput: &FunctionCallInput{
					ID:        "fc_002",
					CallID:    "call_time",
					Name:      "get_time",
					Arguments: `{"tz": "EST"}`,
				},
			},
			{
				Type: "function_call_output",
				FunctionCallOutput: &FunctionCallOutput{
					CallID: "call_weather",
					Output: "Sunny 72F",
				},
			},
			{
				Type: "function_call_output",
				FunctionCallOutput: &FunctionCallOutput{
					CallID: "call_time",
					Output: "3:00 PM EST",
				},
			},
			{Type: "message", Message: &MessageInput{Role: "user", Content: MessageContent{Raw: "Thanks!", IsString: true}}},
		},
	}

	result, err := ConvertRequest(req)
	require.NoError(t, err)

	// user → assistant(2 tool_calls) → tool → tool → user
	require.Len(t, result.Messages, 5)
	assert.Equal(t, "user", result.Messages[0].Role)

	// Two function_call items merged into one assistant message
	assert.Equal(t, "assistant", result.Messages[1].Role)
	require.Len(t, result.Messages[1].ToolCalls, 2)
	assert.Equal(t, "call_weather", result.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "get_weather", result.Messages[1].ToolCalls[0].Function.Name)
	assert.Equal(t, "call_time", result.Messages[1].ToolCalls[1].ID)
	assert.Equal(t, "get_time", result.Messages[1].ToolCalls[1].Function.Name)

	assert.Equal(t, "tool", result.Messages[2].Role)
	assert.Equal(t, "call_weather", result.Messages[2].ToolCallID)
	assert.Equal(t, "tool", result.Messages[3].Role)
	assert.Equal(t, "call_time", result.Messages[3].ToolCallID)
	assert.Equal(t, "user", result.Messages[4].Role)
}

func TestConvertRequest_FunctionCallInputJSONParsing(t *testing.T) {
	t.Parallel()

	jsonStr := `{
		"model": "gpt-4o",
		"input": [
			{"type": "message", "message": {"role": "user", "content": "hi"}},
			{"type": "function_call", "id": "fc_123", "call_id": "call_abc", "name": "shell", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "function_call_output": {"call_id": "call_abc", "output": "file1.txt"}}
		]
	}`

	var req ResponsesAPIRequest
	err := json.Unmarshal([]byte(jsonStr), &req)
	require.NoError(t, err)

	require.Len(t, req.Input, 3)
	assert.Equal(t, "message", req.Input[0].Type)
	assert.Equal(t, "function_call", req.Input[1].Type)
	require.NotNil(t, req.Input[1].FunctionCallInput)
	assert.Equal(t, "fc_123", req.Input[1].FunctionCallInput.ID)
	assert.Equal(t, "call_abc", req.Input[1].FunctionCallInput.CallID)
	assert.Equal(t, "shell", req.Input[1].FunctionCallInput.Name)
	assert.Equal(t, `{"cmd":"ls"}`, req.Input[1].FunctionCallInput.Arguments)

	result, err := ConvertRequest(&req)
	require.NoError(t, err)

	// user → assistant(tool_calls) → tool
	require.Len(t, result.Messages, 3)
	assert.Equal(t, "assistant", result.Messages[1].Role)
	require.Len(t, result.Messages[1].ToolCalls, 1)
	assert.Equal(t, "call_abc", result.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "shell", result.Messages[1].ToolCalls[0].Function.Name)
}
