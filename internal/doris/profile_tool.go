package doris

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/tool"
)

const (
	maxProfileBytes = 256 * 1024 // 256 KB
	profileTimeoutS = 30
	profileAPIPath  = "/rest/v1/query_profile"
)

type profileInput struct {
	QueryID string `json:"query_id"`
}

func newProfileTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "doris.profile",
			Description: "Retrieve the detailed execution profile for a finished Doris query by its query_id. Returns the full operator-level profile text including per-operator ExecTime, rows, memory, and custom counters.",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query_id":{"type":"string","description":"The query_id to retrieve the profile for (e.g. 2deb3a69266b44e6-8860f6d5a82652cc)"}
				},
				"required":["query_id"]
			}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in profileInput
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("invalid doris.profile input: %w", err)
			}
			if in.QueryID == "" {
				return nil, fmt.Errorf("query_id is required")
			}

			cfg, err := LoadConfig(tc.Workspace)
			if err != nil {
				return nil, err
			}

			profileText, err := fetchProfile(ctx, cfg, in.QueryID)
			if err != nil {
				return &tool.Result{
					Text: fmt.Sprintf("(error fetching profile for %s: %v)", in.QueryID, err),
				}, nil
			}

			truncated := len(profileText) >= maxProfileBytes
			return &tool.Result{
				Text:      profileText,
				Truncated: truncated,
			}, nil
		},
	}
}

// fetchProfile retrieves the full profile text for a query via the FE HTTP API.
func fetchProfile(ctx context.Context, cfg Config, queryID string) (string, error) {
	url := fmt.Sprintf("http://%s:%d%s/%s", cfg.Host, cfg.HTTPPort, profileAPIPath, queryID)

	reqCtx, cancel := context.WithTimeout(ctx, profileTimeoutS*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// FE HTTP API uses basic auth with the same credentials as MySQL protocol.
	req.SetBasicAuth(cfg.User, cfg.Password)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Read extra bytes beyond maxProfileBytes to account for JSON envelope overhead.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxProfileBytes*4+4096)))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		// If not JSON, return raw text (some versions return plain text).
		text := string(body)
		if len(text) > maxProfileBytes {
			text = text[:maxProfileBytes]
		}
		return text, nil
	}

	if apiResp.Code != 0 {
		return "", fmt.Errorf("API error: %s (code=%d)", apiResp.Msg, apiResp.Code)
	}

	text := decodeProfileHTML(apiResp.Data)
	if len(text) > maxProfileBytes {
		text = text[:maxProfileBytes]
	}
	return text, nil
}

// decodeProfileHTML converts the HTML-escaped profile text from the Doris FE API
// into plain text. The API returns `&nbsp;&nbsp;` for indentation and `</br>` for
// line breaks.
func decodeProfileHTML(s string) string {
	s = strings.ReplaceAll(s, "</br>", "\n")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return s
}
