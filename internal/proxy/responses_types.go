package proxy

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ResponsesAPIRequest represents a request to the Responses API
// This follows the OpenAI Responses API request format
type ResponsesAPIRequest struct {
	Model               string         `json:"model"`
	Instructions        string         `json:"instructions,omitempty"`
	Input               []InputItem    `json:"input"`
	Stream             *bool          `json:"stream,omitempty"`
	Temperature        *float64       `json:"temperature,omitempty"`
	MaxOutputTokens    *int           `json:"max_output_tokens,omitempty"`
	TopP               *float64       `json:"top_p,omitempty"`
	Tools              []Tool         `json:"tools,omitempty"`
	PreviousResponseID  *string        `json:"previous_response_id,omitempty"`
}

// InputItem represents an item in the input array for Responses API
// This is a union type that can be either a message or a function call output
type InputItem struct {
	Type       string           `json:"type"`
	Message    *MessageInput    `json:"message,omitempty"`
	FunctionCallOutput *FunctionCallOutput `json:"function_call_output,omitempty"`
}

// MessageInput represents a message input item
type MessageInput struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// ContentPart represents a content part in either input or output
type ContentPart struct {
	Type string `json:"type"` // "input_text" or "output_text"
	Text string `json:"text"`
}

// MessageContent can be either a plain string or an array of content parts
type MessageContent struct {
	Raw      string        // set when content is a plain string
	Parts    []ContentPart // set when content is an array of parts
	IsString bool          // true when raw string
}

// UnmarshalJSON implements custom unmarshaling for MessageContent
func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		mc.Raw = s
		mc.IsString = true
		return nil
	}
	// Try array of ContentPart
	var parts []ContentPart
	if err := json.Unmarshal(data, &parts); err == nil {
		mc.Parts = parts
		mc.IsString = false
		return nil
	}
	return fmt.Errorf("content must be a string or array of content parts")
}

// MarshalJSON implements custom marshaling for MessageContent
func (mc MessageContent) MarshalJSON() ([]byte, error) {
	if mc.IsString {
		return json.Marshal(mc.Raw)
	}
	return json.Marshal(mc.Parts)
}

// Text returns the text content regardless of format
func (mc MessageContent) Text() string {
	if mc.IsString {
		return mc.Raw
	}
	var sb strings.Builder
	for _, p := range mc.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// FunctionCallOutput represents output from a function call
type FunctionCallOutput struct {
	CallID    string `json:"call_id"`
	Output    string `json:"output"`
}

// ResponsesAPIResponse represents a response from the Responses API
// This follows the OpenAI Responses API response format
type ResponsesAPIResponse struct {
	ID         string              `json:"id"`
	Object     string              `json:"object"`
	CreatedAt  int64               `json:"created_at"`
	Model      string              `json:"model"`
	Status     string              `json:"status"`
	Output     []OutputItem        `json:"output"`
	Usage      ResponseUsage       `json:"usage"`
}

// OutputItem represents an output item for Responses API
// This is a union type that can be either a message or a function call
type OutputItem struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Message    *OutputMessage   `json:"message,omitempty"`
	FunctionCall *FunctionCall  `json:"function_call,omitempty"`
}

// OutputMessage represents a message output item
type OutputMessage struct {
	Role    string        `json:"role"`
	Content []ContentPart `json:"content"`
}

// FunctionCall represents a function call output
type FunctionCall struct {
	Name      string                 `json:"name"`
	Arguments string                 `json:"arguments"`
	CallID    string                 `json:"call_id,omitempty"`
}

// ResponseUsage represents token usage for Responses API
type ResponseUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// SSE event types for Responses API streaming

// ResponseCreatedEvent represents the initial event when a response starts
type ResponseCreatedEvent struct {
	Type      string                   `json:"type"`
	Response  ResponsesAPIResponse      `json:"response"`
}

// ResponseOutputItemAddedEvent represents when an output item is added
type ResponseOutputItemAddedEvent struct {
	Type    string        `json:"type"`
	Output OutputItem     `json:"output"`
}

// ResponseContentPartAddedEvent represents when a content part is added to an output item
type ResponseContentPartAddedEvent struct {
	Type     string   `json:"type"`
	OutputID string   `json:"output_id"`
	Part     string   `json:"part"`
}

// ResponseOutputTextDeltaEvent represents when text is delta-encoded in an output item
type ResponseOutputTextDeltaEvent struct {
	Type     string `json:"type"`
	OutputID string `json:"output_id"`
	Text     string `json:"text"`
}

// ResponseOutputItemDoneEvent represents when an output item is complete
type ResponseOutputItemDoneEvent struct {
	Type     string    `json:"type"`
	OutputID string    `json:"output_id"`
	Status   string    `json:"status"`
}

// ResponseCompletedEvent represents the final event when a response is complete
type ResponseCompletedEvent struct {
	Type      string              `json:"type"`
	Response  CompletedResponse    `json:"response"`
}

// CompletedResponse represents the final response structure
type CompletedResponse struct {
	ID         string              `json:"id"`
	Object     string              `json:"object"`
	CreatedAt  int64               `json:"created_at"`
	Model      string              `json:"model"`
	Status     string              `json:"status"`
	Output     []OutputItem        `json:"output"`
	Usage      ResponseUsage       `json:"usage"`
}

// ResponsesAPIError represents an error response from the Responses API
type ResponsesAPIError struct {
	Error ResponsesAPIErrorDetail `json:"error"`
}

// ResponsesAPIErrorDetail represents the error details
type ResponsesAPIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	Type    string `json:"type"`
}

// UnmarshalJSON implements custom unmarshaling for InputItem to handle the union type
func (i *InputItem) UnmarshalJSON(data []byte) error {
	type Alias InputItem
	aux := &struct {
		Type string `json:"type"`
	}{Type: "input"} // dummy type to allow unmarshaling
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	i.Type = aux.Type

	switch aux.Type {
	case "message":
		var message MessageInput
		if err := json.Unmarshal(data, &struct {
			Type    string       `json:"type"`
			Message *MessageInput `json:"message"`
		}{Type: aux.Type, Message: &message}); err != nil {
			return err
		}
		i.Message = &message
	case "function_call_output":
		var functionCallOutput FunctionCallOutput
		if err := json.Unmarshal(data, &struct {
			Type              string                `json:"type"`
			FunctionCallOutput *FunctionCallOutput `json:"function_call_output"`
		}{Type: aux.Type, FunctionCallOutput: &functionCallOutput}); err != nil {
			return err
		}
		i.FunctionCallOutput = &functionCallOutput
	default:
		return &json.UnmarshalTypeError{Value: "InputItem", Type: reflect.TypeOf(""), Offset: 0}
	}
	return nil
}

// MarshalJSON implements custom marshaling for InputItem to handle the union type
func (i InputItem) MarshalJSON() ([]byte, error) {
	switch i.Type {
	case "message":
		if i.Message == nil {
			return nil, fmt.Errorf("InputItem of type message: message is nil")
		}
		return json.Marshal(struct {
			Type    string       `json:"type"`
			Message *MessageInput `json:"message"`
		}{
			Type:    i.Type,
			Message: i.Message,
		})
	case "function_call_output":
		if i.FunctionCallOutput == nil {
			return nil, fmt.Errorf("InputItem of type function_call_output: function_call_output is nil")
		}
		return json.Marshal(struct {
			Type              string                `json:"type"`
			FunctionCallOutput *FunctionCallOutput `json:"function_call_output"`
		}{
			Type:              i.Type,
			FunctionCallOutput: i.FunctionCallOutput,
		})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{
			Type: i.Type,
		})
	}
}

// UnmarshalJSON implements custom unmarshaling for OutputItem to handle the union type
func (o *OutputItem) UnmarshalJSON(data []byte) error {
	type Alias OutputItem
	aux := &struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{Type: "output"}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	o.Type = aux.Type
	o.ID = aux.ID

	switch aux.Type {
	case "message":
		var message OutputMessage
		if err := json.Unmarshal(data, &struct {
			ID      string         `json:"id"`
			Type    string         `json:"type"`
			Message *OutputMessage `json:"message"`
		}{ID: aux.ID, Type: aux.Type, Message: &message}); err != nil {
			return err
		}
		o.Message = &message
	case "function_call":
		var functionCall FunctionCall
		if err := json.Unmarshal(data, &struct {
			ID          string       `json:"id"`
			Type        string       `json:"type"`
			FunctionCall *FunctionCall `json:"function_call"`
		}{ID: aux.ID, Type: aux.Type, FunctionCall: &functionCall}); err != nil {
			return err
		}
		o.FunctionCall = &functionCall
	default:
		return &json.UnmarshalTypeError{Value: "OutputItem", Type: reflect.TypeOf(""), Offset: 0}
	}
	return nil
}

// MarshalJSON implements custom marshaling for OutputItem to handle the union type
func (o OutputItem) MarshalJSON() ([]byte, error) {
	switch o.Type {
	case "message":
		if o.Message == nil {
			return nil, fmt.Errorf("OutputItem of type message: message is nil")
		}
		return json.Marshal(struct {
			ID      string         `json:"id"`
			Type    string         `json:"type"`
			Message *OutputMessage `json:"message"`
		}{
			ID:      o.ID,
			Type:    o.Type,
			Message: o.Message,
		})
	case "function_call":
		if o.FunctionCall == nil {
			return nil, fmt.Errorf("OutputItem of type function_call: function_call is nil")
		}
		return json.Marshal(struct {
			ID          string       `json:"id"`
			Type        string       `json:"type"`
			FunctionCall *FunctionCall `json:"function_call"`
		}{
			ID:          o.ID,
			Type:        o.Type,
			FunctionCall: o.FunctionCall,
		})
	default:
		return json.Marshal(struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}{
			ID:   o.ID,
			Type: o.Type,
		})
	}
}

// GenerateItemID generates a unique item ID with the given prefix (e.g. "msg", "fc")
func GenerateItemID(prefix string) string {
	return prefix + "_" + time.Now().Format("20060102150405") + "_" + randomString(8)
}

// GenerateResponseID generates a unique response ID
func GenerateResponseID() string {
	return "resp_" + time.Now().Format("20060102150405") + "_" + randomString(8)
}

// randomString generates a random string of given length
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	randBytes := make([]byte, length)
	_, _ = rand.Read(randBytes)
	for i := range b {
		b[i] = charset[int(randBytes[i])%len(charset)]
	}
	return string(b)
}