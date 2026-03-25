package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/QuakeWang/ori/internal/config"
)

// OpenAIClient implements Client using the openai-go SDK.
// Provider-specific SDK types are fully contained within this file;
// the rest of the codebase only sees the types defined in types.go.
type OpenAIClient struct {
	client    openai.Client
	apiFormat config.APIFormat
}

// NewOpenAIClient creates a Client backed by an OpenAI-compatible API.
func NewOpenAIClient(apiKey, apiBase string, headers map[string]string, apiFormat config.APIFormat) *OpenAIClient {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if apiBase != "" {
		apiBase = normalizeBaseURL(apiBase)
		opts = append(opts, option.WithBaseURL(apiBase))
	}
	for k, v := range headers {
		opts = append(opts, option.WithHeader(k, v))
	}

	return &OpenAIClient{
		client:    openai.NewClient(opts...),
		apiFormat: apiFormat,
	}
}

// normalizeBaseURL ensures the base URL ends with a versioned path (e.g. /v1).
// Many OpenAI-compatible proxies expose their API under /v1/, but users often
// configure just the host (e.g. "https://api.funai.vip"). The openai-go SDK
// appends paths like /chat/completions directly to the base URL, so without
// /v1 the requests hit the wrong endpoint (often the proxy's web UI).
func normalizeBaseURL(base string) string {
	trimmed := strings.TrimRight(base, "/")
	// Already has a versioned suffix like /v1, /v2, etc.
	lastSlash := strings.LastIndex(trimmed, "/")
	if lastSlash >= 0 {
		suffix := trimmed[lastSlash:]
		if len(suffix) >= 3 && suffix[1] == 'v' && suffix[2] >= '0' && suffix[2] <= '9' {
			return trimmed
		}
	}
	slog.Warn("llm.base_url.normalized", "original", base, "resolved", trimmed+"/v1",
		"hint", "appended /v1; set ORI_API_BASE to include /v1 explicitly to suppress this warning")
	return trimmed + "/v1"
}

func (c *OpenAIClient) Chat(ctx context.Context, req Request) (*Response, error) {
	// Apply per-request timeout if specified.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	start := time.Now()
	slog.Info("llm.chat.start", "model", req.Model, "messages", len(req.Messages), "tools", len(req.Tools), "format", c.apiFormat)

	var resp *Response
	var err error

	switch c.apiFormat {
	case config.APIFormatCompletion, "":
		resp, err = c.chatViaCompletions(ctx, req)
	case config.APIFormatResponses:
		resp, err = c.chatViaResponses(ctx, req)
	default:
		return nil, fmt.Errorf("unknown api_format %q", c.apiFormat)
	}

	if err != nil {
		slog.Error("llm.chat.error", "model", req.Model, "elapsed_ms", time.Since(start).Milliseconds(), "error", err)
		return nil, err
	}

	slog.Info("llm.chat.done",
		"model", req.Model,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"finish_reason", resp.FinishReason,
	)
	return resp, nil
}

func (c *OpenAIClient) chatViaCompletions(ctx context.Context, req Request) (*Response, error) {
	params := buildChatParams(req)
	completion, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llm chat (completions) failed: %w", err)
	}
	return fromSDKResponse(completion), nil
}

func (c *OpenAIClient) chatViaResponses(ctx context.Context, req Request) (*Response, error) {
	params := buildResponsesParams(req)
	result, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llm chat (responses) failed: %w", err)
	}
	return fromResponsesAPIResponse(result), nil
}

// ------------------------------------------------------------------ SDK conversion

func buildChatParams(req Request) openai.ChatCompletionNewParams {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)

	// Inject system message if provided.
	if req.System != "" {
		messages = append(messages, openai.SystemMessage(req.System))
	}

	for _, m := range req.Messages {
		messages = append(messages, toSDKMessage(m))
	}

	params := openai.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: messages,
	}

	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	}
	if len(req.Tools) > 0 {
		tools := make([]openai.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, toSDKTool(t))
		}
		params.Tools = tools
	}

	return params
}

func toSDKMessage(m Message) openai.ChatCompletionMessageParamUnion {
	switch m.Role {
	case "system":
		return openai.SystemMessage(m.Content)

	case "user":
		if len(m.Parts) > 0 {
			return toSDKMultimodalUser(m.Parts)
		}
		return openai.UserMessage(m.Content)

	case "assistant":
		if len(m.ToolCalls) > 0 {
			calls := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, openai.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: openai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				})
			}
			msg := openai.AssistantMessage(m.Content)
			msg.OfAssistant.ToolCalls = calls
			return msg
		}
		return openai.AssistantMessage(m.Content)

	case "tool":
		// SDK signature: ToolMessage(content, toolCallID)
		return openai.ToolMessage(m.Content, m.ToolCallID)

	default:
		return openai.UserMessage(m.Content)
	}
}

// toSDKMultimodalUser converts content parts to the SDK's multipart user message format.
func toSDKMultimodalUser(parts []ContentPart) openai.ChatCompletionMessageParamUnion {
	sdkParts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			sdkParts = append(sdkParts, openai.TextContentPart(p.Text))
		case "image_url":
			if p.ImageURL != nil {
				sdkParts = append(sdkParts, openai.ImageContentPart(
					openai.ChatCompletionContentPartImageImageURLParam{
						URL:    p.ImageURL.URL,
						Detail: p.ImageURL.Detail,
					},
				))
			}
		}
	}
	return openai.UserMessage(sdkParts)
}

func toSDKTool(t ToolSchema) openai.ChatCompletionToolParam {
	var params shared.FunctionParameters
	if len(t.Schema) > 0 {
		_ = json.Unmarshal(t.Schema, &params)
	}

	return openai.ChatCompletionToolParam{
		Function: shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  params,
		},
	}
}

func fromSDKResponse(c *openai.ChatCompletion) *Response {
	resp := &Response{
		Usage: Usage{
			InputTokens:  int(c.Usage.PromptTokens),
			OutputTokens: int(c.Usage.CompletionTokens),
			TotalTokens:  int(c.Usage.TotalTokens),
		},
	}

	if len(c.Choices) > 0 {
		ch := c.Choices[0]
		resp.Text = ch.Message.Content
		resp.FinishReason = string(ch.FinishReason)

		for _, tc := range ch.Message.ToolCalls {
			args := json.RawMessage(tc.Function.Arguments)
			if !json.Valid(args) {
				slog.Warn("llm.completions.invalid_tool_arguments",
					"tool", tc.Function.Name,
					"call_id", tc.ID,
					"raw", tc.Function.Arguments)
				args = json.RawMessage(`{}`)
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
	}

	return resp
}

// ------------------------------------------------------------------ Responses API conversion

func buildResponsesParams(req Request) responses.ResponseNewParams {
	// Build input items from messages.
	items := make([]responses.ResponseInputItemUnionParam, 0, len(req.Messages)+1)

	// Inject system message as a system-role input item.
	if req.System != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(
			req.System, responses.EasyInputMessageRoleSystem,
		))
	}

	// The Responses API (without previous_response_id) requires every
	// function_call_output's call_id to reference a function_call in the
	// same request's input list. For multi-turn conversations where prior
	// turns contain tool_call+tool_result pairs, the API cannot validate
	// those historical call_ids and returns a 400 error.
	//
	// Fix: find the boundary of the LAST tool exchange block (the trailing
	// sequence of assistant-with-tool_calls + tool results at the end of
	// the message list). Only that trailing block keeps the native
	// function_call / function_call_output format. Earlier tool exchanges
	// are folded into plain text messages.
	trailingStart := findTrailingToolExchangeStart(req.Messages)

	for i, m := range req.Messages {
		if i < trailingStart {
			items = append(items, toResponsesInputItemFolded(m)...)
		} else {
			items = append(items, toResponsesInputItem(m)...)
		}
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: items,
		},
	}

	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxTokens))
	}

	if len(req.Tools) > 0 {
		tools := make([]responses.ToolUnionParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, toResponsesTool(t))
		}
		params.Tools = tools
	}

	return params
}

// findTrailingToolExchangeStart returns the index where the trailing
// tool exchange block begins. This is the contiguous suffix of messages
// where role is "assistant" with tool_calls or "tool".
// If the messages don't end with a tool exchange, returns len(messages)
// (meaning all messages use the default converter).
func findTrailingToolExchangeStart(msgs []Message) int {
	n := len(msgs)
	i := n
	for i > 0 {
		m := msgs[i-1]
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			i--
		} else {
			break
		}
	}
	return i
}

// toResponsesInputItemFolded converts a message to plain text input items,
// folding assistant tool_calls and tool results into readable text. This is
// used for historical tool exchanges that the Responses API cannot validate.
func toResponsesInputItemFolded(m Message) []responses.ResponseInputItemUnionParam {
	switch m.Role {
	case "assistant":
		if len(m.ToolCalls) > 0 {
			// Fold tool calls into a text summary.
			var b strings.Builder
			if m.Content != "" {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "[Called %s(%s)]\n", tc.Name, string(tc.Arguments))
			}
			return []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage(
					b.String(), responses.EasyInputMessageRoleAssistant,
				),
			}
		}
		return toResponsesInputItem(m) // No tool calls: use normal path.

	case "tool":
		// Fold tool result into an assistant message summarizing the output.
		content := m.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n...(truncated)"
		}
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(
				fmt.Sprintf("[Tool result for %s]: %s", m.ToolCallID, content),
				responses.EasyInputMessageRoleAssistant,
			),
		}

	default:
		return toResponsesInputItem(m) // user/system: use normal path.
	}
}

func toResponsesInputItem(m Message) []responses.ResponseInputItemUnionParam {
	switch m.Role {
	case "system":
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(
				m.Content, responses.EasyInputMessageRoleSystem,
			),
		}
	case "user":
		if len(m.Parts) > 0 {
			return []responses.ResponseInputItemUnionParam{
				toResponsesMultimodalUser(m.Parts),
			}
		}
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(
				m.Content, responses.EasyInputMessageRoleUser,
			),
		}
	case "assistant":
		if len(m.ToolCalls) > 0 {
			var items []responses.ResponseInputItemUnionParam
			// Preserve assistant text before tool calls if present.
			if m.Content != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					m.Content, responses.EasyInputMessageRoleAssistant,
				))
			}
			// Emit each tool call as a separate function_call input item.
			for _, tc := range m.ToolCalls {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(
					string(tc.Arguments), tc.ID, tc.Name,
				))
			}
			return items
		}
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(
				m.Content, responses.EasyInputMessageRoleAssistant,
			),
		}
	case "tool":
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfFunctionCallOutput(
				m.ToolCallID, m.Content,
			),
		}
	default:
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(
				m.Content, responses.EasyInputMessageRoleUser,
			),
		}
	}
}

// toResponsesMultimodalUser converts content parts to Responses API multimodal input.
func toResponsesMultimodalUser(parts []ContentPart) responses.ResponseInputItemUnionParam {
	contentList := make(responses.ResponseInputMessageContentListParam, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			contentList = append(contentList,
				responses.ResponseInputContentParamOfInputText(p.Text),
			)
		case "image_url":
			if p.ImageURL != nil {
				img := responses.ResponseInputContentParamOfInputImage(
					responses.ResponseInputImageDetail(resolveDetail(p.ImageURL.Detail)),
				)
				img.OfInputImage.ImageURL = param.NewOpt(p.ImageURL.URL)
				contentList = append(contentList, img)
			}
		}
	}
	return responses.ResponseInputItemParamOfMessage(
		contentList, responses.EasyInputMessageRoleUser,
	)
}

// resolveDetail returns the image detail level, defaulting to "auto" if empty.
func resolveDetail(detail string) string {
	if detail == "" {
		return "auto"
	}
	return detail
}

func toResponsesTool(t ToolSchema) responses.ToolUnionParam {
	var params map[string]any
	if len(t.Schema) > 0 {
		_ = json.Unmarshal(t.Schema, &params)
	}

	return responses.ToolUnionParam{
		OfFunction: &responses.FunctionToolParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  params,
		},
	}
}

func fromResponsesAPIResponse(r *responses.Response) *Response {
	resp := &Response{
		Usage: Usage{
			InputTokens:  int(r.Usage.InputTokens),
			OutputTokens: int(r.Usage.OutputTokens),
			TotalTokens:  int(r.Usage.TotalTokens),
		},
	}

	// Extract text and tool calls from output items.
	resp.Text = r.OutputText()

	for _, item := range r.Output {
		if item.Type == "function_call" {
			args := json.RawMessage(item.Arguments)
			if !json.Valid(args) {
				slog.Warn("llm.responses.invalid_tool_arguments",
					"tool", item.Name,
					"call_id", item.CallID,
					"raw", item.Arguments)
				args = json.RawMessage(`{}`)
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		}
	}

	// Deterministic FinishReason: tool_calls takes priority over stop.
	switch {
	case len(resp.ToolCalls) > 0:
		resp.FinishReason = "tool_calls"
	case resp.Text != "":
		resp.FinishReason = "stop"
	}

	return resp
}
