package doris

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/tool"
)

const (
	maxProfileBytes      = 2 * 1024 * 1024 // 2 MiB logical payload cap for a single profile view
	profileTimeoutS      = 30
	profileTextAPIPath   = "/api/profile/text" // plain text API — no HTML encoding
	profileBriefAPIPath  = "/rest/v2/manager/query/profile/json"
	defaultProfileView   = profileViewMerged
	briefJSONQuerySuffix = "?is_all_node=false"
)

type profileView string

const (
	profileViewSummary   profileView = "summary"
	profileViewMerged    profileView = "merged"
	profileViewDetail    profileView = "detail"
	profileViewFull      profileView = "full"
	profileViewBriefJSON profileView = "brief_json"
)

type profileInput struct {
	QueryID string `json:"query_id"`
	View    string `json:"view,omitempty"`
}

type fetchedProfile struct {
	Text      string
	Truncated bool
}

type profileSections struct {
	Raw                     string
	Summary                 string
	ExecutionSummary        string
	ChangedSessionVariables string
	PhysicalPlan            string
	MergedProfile           string
	DetailProfile           string
}

func newProfileTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name: "doris.profile",
			Description: "Retrieve Doris query profile views by query_id. " +
				"Supported views: merged (default), detail, summary, full, brief_json.",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query_id":{"type":"string","description":"The query_id to retrieve the profile for (e.g. 2deb3a69266b44e6-8860f6d5a82652cc)"},
					"view":{"type":"string","enum":["summary","merged","detail","full","brief_json"],"description":"Profile view to return. merged is the default Doris-native analysis view."}
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

			view, err := resolveProfileView(in.View)
			if err != nil {
				return nil, err
			}

			cfg, err := LoadConfig(tc.Workspace)
			if err != nil {
				return nil, err
			}

			var fetched fetchedProfile
			switch view {
			case profileViewBriefJSON:
				fetched, err = fetchProfileBriefJSON(ctx, cfg, in.QueryID)
			default:
				fetched, err = fetchProfile(ctx, cfg, in.QueryID)
			}
			if err != nil {
				return &tool.Result{
					Text: fmt.Sprintf("(error fetching profile for %s: %v)", in.QueryID, err),
				}, nil
			}

			text := fetched.Text
			if view != profileViewBriefJSON {
				text = renderProfileView(fetched.Text, view)
			}

			return &tool.Result{
				Text:      text,
				Truncated: fetched.Truncated,
				Meta: map[string]any{
					"view":             string(view),
					"source_truncated": fetched.Truncated,
				},
			}, nil
		},
	}
}

func resolveProfileView(raw string) (profileView, error) {
	trimmed := strings.TrimSpace(raw)
	switch profileView(trimmed) {
	case "":
		return defaultProfileView, nil
	case profileViewSummary, profileViewMerged, profileViewDetail, profileViewFull, profileViewBriefJSON:
		return profileView(trimmed), nil
	default:
		return "", fmt.Errorf("invalid view %q: must be one of summary, merged, detail, full, brief_json", raw)
	}
}

// --------------------------------------------------------------------
// Doris-native profile section rendering
// --------------------------------------------------------------------

// splitProfileSections preserves Doris's native profile layers instead of
// inventing a new summary format:
//   - Summary
//   - Execution Summary
//   - Changed Session Variables
//   - Physical Plan
//   - MergedProfile
//   - DetailProfile (Execution Profile ...)
func splitProfileSections(text string) profileSections {
	lines := strings.Split(text, "\n")

	executionSummaryStart := -1
	changedVarsStart := -1
	physicalPlanStart := -1
	mergedProfileStart := -1
	detailProfileStart := -1

	for i, line := range lines {
		header := normalizeProfileHeader(line)
		switch {
		case strings.HasPrefix(header, "Execution Summary:"):
			if executionSummaryStart < 0 {
				executionSummaryStart = i
			}
		case strings.HasPrefix(header, "Changed Session Variables:"),
			strings.HasPrefix(header, "ChangedSessionVariables:"):
			if changedVarsStart < 0 {
				changedVarsStart = i
			}
		case header == "Physical Plan" || header == "Physical Plan:":
			if physicalPlanStart < 0 {
				physicalPlanStart = i
			}
		case header == "MergedProfile" || header == "MergedProfile:":
			if mergedProfileStart < 0 {
				mergedProfileStart = i
			}
		case strings.HasPrefix(header, "Execution Profile"):
			if detailProfileStart < 0 {
				detailProfileStart = i
			}
		}
	}

	sections := profileSections{Raw: strings.Trim(text, "\n")}
	if len(lines) == 0 {
		return sections
	}

	summaryEnd := minPositive(len(lines), executionSummaryStart, changedVarsStart, physicalPlanStart, mergedProfileStart, detailProfileStart)
	sections.Summary = sliceProfileLines(lines, 0, summaryEnd)

	if executionSummaryStart >= 0 {
		sections.ExecutionSummary = sliceProfileLines(
			lines,
			executionSummaryStart,
			minPositive(len(lines), changedVarsStart, physicalPlanStart, mergedProfileStart, detailProfileStart),
		)
	}
	if changedVarsStart >= 0 {
		sections.ChangedSessionVariables = sliceProfileLines(
			lines,
			changedVarsStart,
			minPositive(len(lines), physicalPlanStart, mergedProfileStart, detailProfileStart),
		)
	}
	if physicalPlanStart >= 0 {
		sections.PhysicalPlan = sliceProfileLines(
			lines,
			physicalPlanStart,
			minPositive(len(lines), mergedProfileStart, detailProfileStart),
		)
	}
	if mergedProfileStart >= 0 {
		sections.MergedProfile = sliceProfileLines(lines, mergedProfileStart, minPositive(len(lines), detailProfileStart))
	}
	if detailProfileStart >= 0 {
		sections.DetailProfile = sliceProfileLines(lines, detailProfileStart, len(lines))
	}

	return sections
}

func renderProfileView(text string, view profileView) string {
	sections := splitProfileSections(text)

	switch view {
	case profileViewSummary:
		rendered := joinProfileParts(
			sections.Summary,
			sections.ExecutionSummary,
			sections.ChangedSessionVariables,
		)
		if rendered == "" {
			return sections.Raw
		}
		return rendered

	case profileViewMerged:
		if sections.MergedProfile != "" {
			return joinProfileParts(
				sections.Summary,
				sections.ExecutionSummary,
				sections.ChangedSessionVariables,
				sections.PhysicalPlan,
				sections.MergedProfile,
			)
		}
		// profile_level != 1 may not emit a merged profile. In that case return
		// the available sections plus detail so the caller still gets a useful view.
		rendered := joinProfileParts(
			sections.Summary,
			sections.ExecutionSummary,
			sections.ChangedSessionVariables,
			sections.PhysicalPlan,
			sections.DetailProfile,
		)
		if rendered == "" {
			return sections.Raw
		}
		return appendProfileNote(rendered, "[MergedProfile unavailable; returning available detail sections instead]")

	case profileViewDetail:
		if sections.DetailProfile != "" {
			return sections.DetailProfile
		}
		if sections.MergedProfile != "" {
			return appendProfileNote(sections.MergedProfile, "[DetailProfile unavailable; returning MergedProfile instead]")
		}
		return sections.Raw

	case profileViewFull:
		fallthrough
	default:
		return sections.Raw
	}
}

func normalizeProfileHeader(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

func minPositive(defaultValue int, candidates ...int) int {
	min := defaultValue
	for _, candidate := range candidates {
		if candidate >= 0 && candidate < min {
			min = candidate
		}
	}
	return min
}

func sliceProfileLines(lines []string, start, end int) string {
	if start < 0 || start >= len(lines) || end <= start {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Trim(strings.Join(lines[start:end], "\n"), "\n")
}

func joinProfileParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "\n")
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n\n")
}

func appendProfileNote(text, note string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return note
	}
	return text + "\n\n" + note
}

// --------------------------------------------------------------------
// Profile fetching
// --------------------------------------------------------------------

// fetchProfile retrieves the Doris full text profile via the plain-text HTTP API.
// Uses /api/profile/text which returns the profile without HTML encoding,
// eliminating the double-space bug caused by the v1 API's "&nbsp;&nbsp;" encoding.
func fetchProfile(ctx context.Context, cfg Config, queryID string) (fetchedProfile, error) {
	apiURL := fmt.Sprintf(
		"http://%s:%d%s?query_id=%s",
		cfg.Host, cfg.HTTPPort, profileTextAPIPath, neturl.QueryEscape(queryID),
	)

	reqCtx, cancel := context.WithTimeout(ctx, profileTimeoutS*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("create request: %w", err)
	}

	// FE HTTP API uses basic auth with the same credentials as MySQL protocol.
	req.SetBasicAuth(cfg.User, cfg.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("http request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fetchedProfile{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// /api/profile/text returns the profile as plain text directly (no JSON envelope).
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxProfileBytes+4096)))
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("read response: %w", err)
	}

	text := strings.TrimSpace(string(body))
	if text == "" || strings.HasPrefix(text, "query id") {
		return fetchedProfile{}, fmt.Errorf("profile not found: %s", text)
	}

	return capFetchedProfile(text), nil
}

// fetchProfileBriefJSON retrieves Doris's structured brief profile JSON via the
// v2 manager API and pretty-prints it for inspection.
func fetchProfileBriefJSON(ctx context.Context, cfg Config, queryID string) (fetchedProfile, error) {
	url := fmt.Sprintf(
		"http://%s:%d%s/%s%s",
		cfg.Host, cfg.HTTPPort, profileBriefAPIPath, neturl.PathEscape(queryID), briefJSONQuerySuffix,
	)

	reqCtx, cancel := context.WithTimeout(ctx, profileTimeoutS*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("create request: %w", err)
	}
	req.SetBasicAuth(cfg.User, cfg.Password)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("http request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fetchedProfile{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxProfileBytes*2+4096)))
	if err != nil {
		return fetchedProfile{}, fmt.Errorf("read response: %w", err)
	}

	var apiResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Profile string `json:"profile"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fetchedProfile{}, fmt.Errorf("parse brief profile response: %w", err)
	}
	if apiResp.Code != 0 {
		return fetchedProfile{}, fmt.Errorf("API error: %s (code=%d)", apiResp.Msg, apiResp.Code)
	}
	if strings.TrimSpace(apiResp.Data.Profile) == "" {
		return fetchedProfile{}, fmt.Errorf("brief profile is empty")
	}

	return capFetchedProfile(prettyJSON(apiResp.Data.Profile)), nil
}

func capFetchedProfile(text string) fetchedProfile {
	truncated := len(text) > maxProfileBytes
	if truncated {
		text = text[:maxProfileBytes]
	}
	return fetchedProfile{
		Text:      text,
		Truncated: truncated,
	}
}

func prettyJSON(raw string) string {
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(raw), "", "  "); err == nil {
		return out.String()
	}
	return raw
}


