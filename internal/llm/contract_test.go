package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ mock client

// mockClient is a test double that records calls and returns canned responses.
type mockClient struct {
	name     string
	response *Response
	err      error
	calls    []Request // records each call's request
}

func (m *mockClient) Chat(_ context.Context, req Request) (*Response, error) {
	m.calls = append(m.calls, req)
	return m.response, m.err
}

// ------------------------------------------------------------------ ProviderRouter.Chat routing

func TestProviderRouter_Chat_RoutesToCorrectProvider(t *testing.T) {
	openaiClient := &mockClient{
		name:     "openai",
		response: &Response{Text: "from-openai", FinishReason: "stop"},
	}
	orClient := &mockClient{
		name:     "openrouter",
		response: &Response{Text: "from-openrouter", FinishReason: "stop"},
	}

	router := &ProviderRouter{
		clients: map[string]Client{
			"openai":     openaiClient,
			"openrouter": orClient,
		},
		defaultClient: openaiClient,
	}

	// Route to openrouter via model prefix.
	resp, err := router.Chat(context.Background(), Request{Model: "openrouter:qwen/qwen3-coder-next"})
	require.NoError(t, err)
	assert.Equal(t, "from-openrouter", resp.Text)
	// Model should be stripped of the provider prefix.
	require.Len(t, orClient.calls, 1)
	assert.Equal(t, "qwen/qwen3-coder-next", orClient.calls[0].Model)

	// Route to openai via model prefix.
	resp, err = router.Chat(context.Background(), Request{Model: "openai:gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "from-openai", resp.Text)
	require.Len(t, openaiClient.calls, 1)
	assert.Equal(t, "gpt-4o", openaiClient.calls[0].Model)
}

func TestProviderRouter_Chat_FallsBackToDefault(t *testing.T) {
	defaultClient := &mockClient{
		response: &Response{Text: "from-default", FinishReason: "stop"},
	}

	router := &ProviderRouter{
		clients:       map[string]Client{},
		defaultClient: defaultClient,
	}

	// Model without provider prefix -> default client.
	resp, err := router.Chat(context.Background(), Request{Model: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "from-default", resp.Text)
	assert.Equal(t, "gpt-4o", defaultClient.calls[0].Model)
}

func TestProviderRouter_Chat_ExplicitProviderDoesNotFallbackToDefault(t *testing.T) {
	defaultClient := &mockClient{
		response: &Response{Text: "from-default", FinishReason: "stop"},
	}

	router := &ProviderRouter{
		clients:       map[string]Client{},
		defaultClient: defaultClient,
	}

	testCases := []string{
		"unknown:some-model",
		"openai:gpt-5.4",
		"openrouter:qwen/qwen3-coder-next",
	}

	for _, model := range testCases {
		_, err := router.Chat(context.Background(), Request{Model: model})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no client for provider")
	}

	assert.Empty(t, defaultClient.calls, "default client must not be used for explicit providers")
}

func TestProviderRouter_Chat_NoClientForProvider(t *testing.T) {
	router := &ProviderRouter{
		clients:       map[string]Client{},
		defaultClient: nil,
	}

	_, err := router.Chat(context.Background(), Request{Model: "unknown:some-model"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestProviderRouter_Chat_FallbackOnError(t *testing.T) {
	primary := &mockClient{err: fmt.Errorf("primary failed")}
	fallback := &mockClient{
		response: &Response{Text: "from-fallback", FinishReason: "stop"},
	}

	router := &ProviderRouter{
		clients: map[string]Client{
			"openai":     primary,
			"openrouter": fallback,
		},
		fallbackModels: []string{"openrouter:qwen/qwen3"},
	}

	resp, err := router.Chat(context.Background(), Request{Model: "openai:gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "from-fallback", resp.Text)

	// Primary should have been called once.
	require.Len(t, primary.calls, 1)
	// Fallback should have been called once with the stripped model name.
	require.Len(t, fallback.calls, 1)
	assert.Equal(t, "qwen/qwen3", fallback.calls[0].Model)
}

func TestProviderRouter_Chat_AllFallbacksFail(t *testing.T) {
	primary := &mockClient{err: fmt.Errorf("primary failed")}
	first := &mockClient{err: fmt.Errorf("fallback-1 failed")}
	second := &mockClient{err: fmt.Errorf("fallback-2 failed")}

	router := &ProviderRouter{
		clients: map[string]Client{
			"openai": primary,
			"first":  first,
			"second": second,
		},
		fallbackModels: []string{"first:model-a", "second:model-b"},
	}

	_, err := router.Chat(context.Background(), Request{Model: "openai:gpt-4o"})
	require.Error(t, err)
	// Should return the *primary* error, not a fallback error.
	assert.Contains(t, err.Error(), "primary failed")
}

// ------------------------------------------------------------------ buildChatParams conversion

func TestBuildChatParams_SystemInjection(t *testing.T) {
	req := Request{
		Model:  "gpt-4o",
		System: "You are a helpful assistant",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	params := buildChatParams(req)
	assert.Equal(t, "gpt-4o", params.Model)
	// System message + user message = 2 messages.
	assert.Len(t, params.Messages, 2)
}

func TestBuildChatParams_NoSystem(t *testing.T) {
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	params := buildChatParams(req)
	assert.Len(t, params.Messages, 1)
}

func TestBuildChatParams_MaxTokens(t *testing.T) {
	req := Request{
		Model:     "gpt-4o",
		MaxTokens: 2048,
		Messages:  []Message{{Role: "user", Content: "Hi"}},
	}

	params := buildChatParams(req)
	assert.Equal(t, int64(2048), params.MaxTokens.Value)
}

func TestBuildChatParams_WithTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	req := Request{
		Model: "gpt-4o",
		Tools: []ToolSchema{
			{Name: "bash", Description: "Run a bash command", Schema: schema},
		},
		Messages: []Message{{Role: "user", Content: "Run ls"}},
	}

	params := buildChatParams(req)
	require.Len(t, params.Tools, 1)
	assert.Equal(t, "bash", params.Tools[0].Function.Name)
}

// ------------------------------------------------------------------ toSDKMessage conversion

func TestToSDKMessage_UserText(t *testing.T) {
	m := Message{Role: "user", Content: "hello"}
	msg := toSDKMessage(m)
	// Should produce a user message (not panicking, correct type).
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
}

func TestToSDKMessage_AssistantWithToolCalls(t *testing.T) {
	m := Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
		},
	}
	msg := toSDKMessage(m)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "call_1")
	assert.Contains(t, string(data), "bash")
}

func TestToSDKMessage_ToolResult(t *testing.T) {
	m := Message{Role: "tool", ToolCallID: "call_1", Content: "file1.txt\nfile2.txt"}
	msg := toSDKMessage(m)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "call_1")
	assert.Contains(t, string(data), "file1.txt")
}

// ------------------------------------------------------------------ toSDKMultimodalUser conversion

func TestToSDKMultimodalUser_TextAndImage(t *testing.T) {
	parts := []ContentPart{
		{Type: "text", Text: "describe this image"},
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png"}},
	}
	msg := toSDKMultimodalUser(parts)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "describe this image")
	assert.Contains(t, string(data), "https://example.com/img.png")
}

func TestToSDKMultimodalUser_NilImageURL(t *testing.T) {
	parts := []ContentPart{
		{Type: "text", Text: "just text"},
		{Type: "image_url", ImageURL: nil}, // should be skipped
	}
	msg := toSDKMultimodalUser(parts)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "just text")
}

func TestToSDKMultimodalUser_DetailPreserved(t *testing.T) {
	parts := []ContentPart{
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png", Detail: "high"}},
	}
	msg := toSDKMultimodalUser(parts)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"detail":"high"`)
}

func TestToSDKMultimodalUser_EmptyDetailOmitted(t *testing.T) {
	parts := []ContentPart{
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png"}},
	}
	msg := toSDKMultimodalUser(parts)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	// SDK's omitzero means empty string Detail is omitted from JSON.
	assert.NotContains(t, string(data), `"detail"`)
}

// ------------------------------------------------------------------ toSDKTool conversion

func TestToSDKTool_SchemaRoundTrip(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	ts := ToolSchema{
		Name:        "read_file",
		Description: "Read a file",
		Schema:      schema,
	}

	tool := toSDKTool(ts)
	assert.Equal(t, "read_file", tool.Function.Name)

	// Verify the schema was properly unmarshalled.
	params := tool.Function.Parameters
	require.NotNil(t, params)

	// It should be a map with "type", "properties", "required".
	paramBytes, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(paramBytes), "path")
	assert.Contains(t, string(paramBytes), "required")
}

func TestToSDKTool_EmptySchema(t *testing.T) {
	ts := ToolSchema{Name: "noop", Description: "No-op tool"}
	tool := toSDKTool(ts)
	assert.Equal(t, "noop", tool.Function.Name)
}

// ------------------------------------------------------------------ fromSDKResponse extraction

func TestFromSDKResponse_TextOnly(t *testing.T) {
	completion := &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Content: "Hello, world!",
				},
				FinishReason: "stop",
			},
		},
		Usage: openai.CompletionUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	resp := fromSDKResponse(completion)
	assert.Equal(t, "Hello, world!", resp.Text)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
	assert.Empty(t, resp.ToolCalls)
	assert.False(t, resp.IsToolCall())
}

func TestFromSDKResponse_WithToolCalls(t *testing.T) {
	completion := &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					ToolCalls: []openai.ChatCompletionMessageToolCall{
						{
							ID: "call_abc",
							Function: openai.ChatCompletionMessageToolCallFunction{
								Name:      "bash",
								Arguments: `{"cmd":"echo hi"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: openai.CompletionUsage{
			PromptTokens:     20,
			CompletionTokens: 10,
			TotalTokens:      30,
		},
	}

	resp := fromSDKResponse(completion)
	assert.True(t, resp.IsToolCall())
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	assert.Equal(t, "bash", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"cmd":"echo hi"}`, string(resp.ToolCalls[0].Arguments))
}

func TestFromSDKResponse_EmptyChoices(t *testing.T) {
	completion := &openai.ChatCompletion{
		Choices: []openai.ChatCompletionChoice{},
	}

	resp := fromSDKResponse(completion)
	assert.Equal(t, "", resp.Text)
	assert.Equal(t, "", resp.FinishReason)
	assert.Empty(t, resp.ToolCalls)
}

// ------------------------------------------------------------------ fromResponsesAPIResponse extraction

func TestFromResponsesAPIResponse_TextOnly(t *testing.T) {
	// Simulate a responses API result by unmarshalling JSON.
	raw := `{
		"id": "resp_1",
		"output": [
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Hello from responses API"}
				]
			}
		],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5,
			"total_tokens": 15,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`

	var r responses.Response
	err := json.Unmarshal([]byte(raw), &r)
	require.NoError(t, err)

	resp := fromResponsesAPIResponse(&r)
	assert.Equal(t, "Hello from responses API", resp.Text)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
	assert.Empty(t, resp.ToolCalls)
}

func TestFromResponsesAPIResponse_WithFunctionCall(t *testing.T) {
	raw := `{
		"id": "resp_2",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_xyz",
				"name": "bash",
				"arguments": "{\"cmd\":\"echo hi\"}"
			}
		],
		"usage": {
			"input_tokens": 20,
			"output_tokens": 10,
			"total_tokens": 30,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`

	var r responses.Response
	err := json.Unmarshal([]byte(raw), &r)
	require.NoError(t, err)

	resp := fromResponsesAPIResponse(&r)
	assert.True(t, resp.IsToolCall())
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_xyz", resp.ToolCalls[0].ID)
	assert.Equal(t, "bash", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"cmd":"echo hi"}`, string(resp.ToolCalls[0].Arguments))
	assert.Equal(t, "tool_calls", resp.FinishReason)
}

func TestFromResponsesAPIResponse_MixedOutput(t *testing.T) {
	// Output: message THEN function_call. FinishReason should be tool_calls.
	raw := `{
		"id": "resp_3",
		"output": [
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Let me run that for you."}
				]
			},
			{
				"type": "function_call",
				"call_id": "call_123",
				"name": "bash",
				"arguments": "{\"cmd\":\"ls\"}"
			}
		],
		"usage": {
			"input_tokens": 15,
			"output_tokens": 8,
			"total_tokens": 23,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`

	var r responses.Response
	err := json.Unmarshal([]byte(raw), &r)
	require.NoError(t, err)

	resp := fromResponsesAPIResponse(&r)
	assert.Equal(t, "Let me run that for you.", resp.Text)
	assert.True(t, resp.IsToolCall())
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	// tool_calls takes priority over stop, regardless of output order.
	assert.Equal(t, "tool_calls", resp.FinishReason)
}

func TestFromResponsesAPIResponse_ReverseOrder(t *testing.T) {
	// Output: function_call THEN message. FinishReason should still be tool_calls.
	raw := `{
		"id": "resp_reverse",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_first",
				"name": "bash",
				"arguments": "{\"cmd\":\"pwd\"}"
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Done."}
				]
			}
		],
		"usage": {
			"input_tokens": 5,
			"output_tokens": 3,
			"total_tokens": 8,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`

	var r responses.Response
	err := json.Unmarshal([]byte(raw), &r)
	require.NoError(t, err)

	resp := fromResponsesAPIResponse(&r)
	assert.True(t, resp.IsToolCall())
	// Must be tool_calls even though message comes AFTER function_call.
	assert.Equal(t, "tool_calls", resp.FinishReason)
}

func TestFromResponsesAPIResponse_EmptyOutput(t *testing.T) {
	raw := `{
		"id": "resp_4",
		"output": [],
		"usage": {
			"input_tokens": 0,
			"output_tokens": 0,
			"total_tokens": 0,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`

	var r responses.Response
	err := json.Unmarshal([]byte(raw), &r)
	require.NoError(t, err)

	resp := fromResponsesAPIResponse(&r)
	assert.Equal(t, "", resp.Text)
	assert.Equal(t, "", resp.FinishReason)
	assert.Empty(t, resp.ToolCalls)
}

// ------------------------------------------------------------------ buildResponsesParams conversion

func TestBuildResponsesParams_SystemAndMessages(t *testing.T) {
	req := Request{
		Model:  "gpt-4o",
		System: "You are a helpful assistant",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "What's 2+2?"},
		},
		MaxTokens: 1024,
	}

	params := buildResponsesParams(req)
	assert.Equal(t, "gpt-4o", string(params.Model))
	// System (1 item) + user (1) + assistant (1) + user (1) = 4 input items.
	assert.Len(t, params.Input.OfInputItemList, 4)
	assert.Equal(t, int64(1024), params.MaxOutputTokens.Value)
}

func TestBuildResponsesParams_AssistantToolCallHistory(t *testing.T) {
	// An assistant message with 2 tool calls should produce 2 function_call items.
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "Run ls and pwd"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "call_1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
					{ID: "call_2", Name: "bash", Arguments: json.RawMessage(`{"cmd":"pwd"}`)},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "file1.txt"},
			{Role: "tool", ToolCallID: "call_2", Content: "/home/user"},
		},
	}

	params := buildResponsesParams(req)
	// user (1) + 2 function_calls + 2 function_call_outputs = 5.
	assert.Len(t, params.Input.OfInputItemList, 5)

	// Verify the function_call items contain the tool call data.
	data, err := json.Marshal(params.Input.OfInputItemList[1])
	require.NoError(t, err)
	assert.Contains(t, string(data), "call_1")
	assert.Contains(t, string(data), "bash")
}

func TestBuildResponsesParams_MixedAssistantReEncoding(t *testing.T) {
	// An assistant message with BOTH text AND tool calls.
	// Must produce: 1 assistant message + N function_call items.
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "Analyze this"},
			{
				Role:    "assistant",
				Content: "Let me run some commands.",
				ToolCalls: []ToolCall{
					{ID: "call_x", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
				},
			},
			{Role: "tool", ToolCallID: "call_x", Content: "file1.txt"},
		},
	}

	params := buildResponsesParams(req)
	// user (1) + assistant-text (1) + function_call (1) + function_call_output (1) = 4.
	require.Len(t, params.Input.OfInputItemList, 4)

	// First item after user should be the assistant text message.
	data1, err := json.Marshal(params.Input.OfInputItemList[1])
	require.NoError(t, err)
	assert.Contains(t, string(data1), "Let me run some commands")

	// Second item should be a function_call.
	data2, err := json.Marshal(params.Input.OfInputItemList[2])
	require.NoError(t, err)
	assert.Contains(t, string(data2), "call_x")
	assert.Contains(t, string(data2), "bash")
}

func TestBuildResponsesParams_MultimodalUser(t *testing.T) {
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Parts: []ContentPart{
					{Type: "text", Text: "describe this image"},
					{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png"}},
				},
			},
		},
	}

	params := buildResponsesParams(req)
	require.Len(t, params.Input.OfInputItemList, 1)

	// The item should be a message with content list containing text + image.
	data, err := json.Marshal(params.Input.OfInputItemList[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), "describe this image")
	assert.Contains(t, string(data), "https://example.com/img.png")
}

func TestBuildResponsesParams_MultimodalUser_DetailPreserved(t *testing.T) {
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Parts: []ContentPart{
					{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png", Detail: "low"}},
				},
			},
		},
	}

	params := buildResponsesParams(req)
	require.Len(t, params.Input.OfInputItemList, 1)
	data, err := json.Marshal(params.Input.OfInputItemList[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), `"detail":"low"`)
}

func TestBuildResponsesParams_MultimodalUser_EmptyDetailDefaultsToAuto(t *testing.T) {
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Parts: []ContentPart{
					{Type: "image_url", ImageURL: &ImageURLPart{URL: "https://example.com/img.png"}},
				},
			},
		},
	}

	params := buildResponsesParams(req)
	require.Len(t, params.Input.OfInputItemList, 1)
	data, err := json.Marshal(params.Input.OfInputItemList[0])
	require.NoError(t, err)
	// Empty detail should be resolved to "auto" for the Responses API.
	assert.Contains(t, string(data), `"detail":"auto"`)
}

func TestBuildResponsesParams_WithTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	req := Request{
		Model: "gpt-4o",
		Tools: []ToolSchema{
			{Name: "bash", Description: "Run bash", Schema: schema},
			{Name: "read_file", Description: "Read a file", Schema: json.RawMessage(`{}`)},
		},
		Messages: []Message{{Role: "user", Content: "Run ls"}},
	}

	params := buildResponsesParams(req)
	require.Len(t, params.Tools, 2)
	assert.NotNil(t, params.Tools[0].OfFunction)
	assert.Equal(t, "bash", params.Tools[0].OfFunction.Name)
	assert.Equal(t, "read_file", params.Tools[1].OfFunction.Name)
}

func TestBuildResponsesParams_ToolResult(t *testing.T) {
	req := Request{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "tool", ToolCallID: "call_1", Content: "file1.txt"},
		},
	}

	params := buildResponsesParams(req)
	require.Len(t, params.Input.OfInputItemList, 1)
	// The tool result should be a function_call_output item.
	data, err := json.Marshal(params.Input.OfInputItemList[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), "function_call_output")
	assert.Contains(t, string(data), "call_1")
}

// ------------------------------------------------------------------ toResponsesTool conversion

func TestToResponsesTool_SchemaRoundTrip(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	ts := ToolSchema{
		Name:        "read_file",
		Description: "Read a file",
		Schema:      schema,
	}

	tool := toResponsesTool(ts)
	require.NotNil(t, tool.OfFunction)
	assert.Equal(t, "read_file", tool.OfFunction.Name)

	// Verify schema was marshalled into Parameters.
	paramBytes, err := json.Marshal(tool.OfFunction.Parameters)
	require.NoError(t, err)
	assert.Contains(t, string(paramBytes), "path")
}
