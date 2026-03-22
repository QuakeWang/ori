package doris

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/tool"
)

const defaultMaxRows = 100

type pingInput struct{}

type queryOutputOptions struct {
	MaxRows  int    `json:"max_rows"`
	Database string `json:"database"`
	Columns  string `json:"columns"`
}

type sqlInput struct {
	SQL string `json:"sql"`
	queryOutputOptions
}

type sqlSessionInput struct {
	SetupSQLs []string `json:"setup_sqls"`
	SQL       string   `json:"sql"`
	queryOutputOptions
}

// RegisterTools registers Doris extension tools into the shared registry.
func RegisterTools(r *tool.Registry) error {
	tools := []*tool.Tool{
		newPingTool(Open),
		newSQLTool(Open),
		newSQLSessionTool(Open),
		newProfileTool(),
	}

	for _, item := range tools {
		if err := r.Register(item); err != nil {
			return err
		}
	}
	return nil
}

func newPingTool(open OpenFunc) *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "doris.ping",
			Description: "Test the connection to the Doris cluster and return basic cluster info.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in pingInput
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return nil, fmt.Errorf("invalid doris.ping input: %w", err)
				}
			}

			client, cfg, err := openWorkspaceConn(tc.Workspace, "", open)
			if err != nil {
				return nil, err
			}
			defer func() { _ = client.Close() }()

			info, err := client.Ping(ctx)
			if err != nil {
				return nil, fmt.Errorf("doris connection test failed: %w", err)
			}

			return &tool.Result{
				Text: formatPingSummary(cfg, info),
			}, nil
		},
	}
}

func newSQLTool(open OpenFunc) *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "doris.sql",
			Description: "Execute a read-only SQL query against Doris with skill-aware safety checks.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string","description":"The SQL statement to execute. Only read-only statements are allowed."},"max_rows":{"type":"integer","description":"Maximum rows to display in the output."},"database":{"type":"string","description":"Optional database to use for the query."},"columns":{"type":"string","description":"Optional comma-separated column names to include in the output."}},"required":["sql"]}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in sqlInput
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("invalid doris.sql input: %w", err)
			}

			return runReadOnlyQueryTool(ctx, tc, open, in.SQL, in.queryOutputOptions, nil, func(ctx context.Context, client Conn, query string) (*ResultSet, error) {
				return client.Query(ctx, query)
			})
		},
	}
}

type OpenFunc func(cfg Config) (Conn, error)

// validateAndConnect validates a read-only SQL query, checks blocked patterns,
// loads config, and opens a connection. Returns (conn, trimmedQuery, nil, nil)
// on success. On failure returns (nil, "", result, err) where result may be
// non-nil for blocked queries.
func validateAndConnect(
	tc *tool.Context, open OpenFunc,
	rawSQL, database string, patterns []*regexp.Regexp,
) (Conn, string, *tool.Result, error) {
	patterns, err := resolveBlockedPatterns(tc.State, patterns)
	if err != nil {
		return nil, "", nil, err
	}

	query, blockedResult, err := validateReadOnlyQuery(rawSQL, patterns)
	if blockedResult != nil || err != nil {
		return nil, "", blockedResult, err
	}

	client, _, err := openWorkspaceConn(tc.Workspace, database, open)
	if err != nil {
		return nil, "", nil, err
	}
	return client, query, nil, nil
}

func openWorkspaceConn(workspace, database string, open OpenFunc) (Conn, Config, error) {
	cfg, err := LoadConfigWithDatabase(workspace, database)
	if err != nil {
		return nil, Config{}, err
	}
	client, err := open(cfg)
	if err != nil {
		return nil, Config{}, fmt.Errorf("doris connection failed: %w", err)
	}
	return client, cfg, nil
}

func runReadOnlyQueryTool(
	ctx context.Context,
	tc *tool.Context,
	open OpenFunc,
	rawSQL string,
	options queryOutputOptions,
	patterns []*regexp.Regexp,
	run func(context.Context, Conn, string) (*ResultSet, error),
) (*tool.Result, error) {
	client, query, result, err := validateAndConnect(tc, open, rawSQL, options.Database, patterns)
	if client == nil {
		return result, err
	}
	defer func() { _ = client.Close() }()

	return runQueryAndFormat(query, options.MaxRows, options.Columns, func() (*ResultSet, error) {
		return run(ctx, client, query)
	})
}

func runQueryAndFormat(
	query string,
	maxRows int,
	columnsRaw string,
	run func() (*ResultSet, error),
) (*tool.Result, error) {
	resultSet, err := run()
	if err != nil {
		return &tool.Result{Text: formatQueryError(err, query)}, nil
	}
	return formatQueryResult(resultSet, maxRows, columnsRaw), nil
}

// formatQueryResult formats a ResultSet into a tool.Result with truncation.
func formatQueryResult(rs *ResultSet, maxRows int, columnsRaw string) *tool.Result {
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	columns := splitColumns(columnsRaw)
	return &tool.Result{
		Text:      FormatResultSet(rs, maxRows, columns),
		Truncated: len(rs.Rows) > maxRows,
	}
}

func splitColumns(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			columns = append(columns, part)
		}
	}
	return columns
}

func previewSQL(query string, limit int) string {
	query = strings.TrimSpace(query)
	if len(query) <= limit {
		return query
	}
	return query[:limit]
}

func formatQueryError(err error, query string) string {
	return fmt.Sprintf("(error: %T: %v\nSQL: %s)", err, err, previewSQL(query, 80))
}

func newSQLSessionTool(open OpenFunc) *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "doris.session",
			Description: "Execute session-local SET assignments followed by a read-only SQL query within a single database session. Use this when you need session variables like enable_profile to persist for the query.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"setup_sqls":{"type":"array","items":{"type":"string"},"description":"Session-local SET assignments to execute before the query (e.g. SET enable_profile = true). Only single session-variable SET assignments are allowed."},"sql":{"type":"string","description":"The read-only SQL query to execute after setup."},"max_rows":{"type":"integer","description":"Maximum rows to display."},"database":{"type":"string","description":"Optional database."},"columns":{"type":"string","description":"Optional comma-separated column names."}},"required":["setup_sqls","sql"]}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in sqlSessionInput
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("invalid doris.session input: %w", err)
			}

			patterns, err := resolveBlockedPatterns(tc.State, nil)
			if err != nil {
				return nil, err
			}
			blockedResult, err := validateSetupSQLs(in.SetupSQLs, patterns)
			if blockedResult != nil || err != nil {
				return blockedResult, err
			}

			return runReadOnlyQueryTool(ctx, tc, open, in.SQL, in.queryOutputOptions, patterns, func(ctx context.Context, client Conn, query string) (*ResultSet, error) {
				return client.SessionQuery(ctx, in.SetupSQLs, query)
			})
		},
	}
}

func formatPingSummary(cfg Config, info PingInfo) string {
	return fmt.Sprintf(
		"OK: connected to %s:%d\nuser: %s | database: %s | version: %s",
		cfg.Host,
		cfg.Port,
		displayValue(info.User, "?"),
		displayValue(info.Database, "(none)"),
		displayValue(info.Version, "?"),
	)
}

func displayValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func resolveBlockedPatterns(state *session.State, patterns []*regexp.Regexp) ([]*regexp.Regexp, error) {
	if patterns != nil {
		return patterns, nil
	}
	return BlockedPatternsFromState(state)
}

func validateReadOnlyQuery(rawSQL string, patterns []*regexp.Regexp) (string, *tool.Result, error) {
	query := strings.TrimSpace(rawSQL)
	if query == "" {
		return "", nil, fmt.Errorf("sql is required")
	}
	if !IsReadOnly(query) {
		return "", nil, fmt.Errorf(
			"only read-only SQL is allowed (SELECT, SHOW, DESCRIBE, EXPLAIN, ADMIN SHOW, ADMIN DIAGNOSE, HELP). got: %s",
			previewSQL(query, 80),
		)
	}

	blockedResult, err := validateSkillSQL(query, patterns)
	if blockedResult != nil || err != nil {
		return "", blockedResult, err
	}
	return query, nil, nil
}

func validateSetupSQLs(setupSQLs []string, patterns []*regexp.Regexp) (*tool.Result, error) {
	for _, setup := range setupSQLs {
		if !IsSetStatement(setup) {
			return nil, fmt.Errorf("setup_sqls only allows single session variable SET assignments, got: %s", previewSQL(setup, 80))
		}
		blockedResult, err := validateSkillSQL(setup, patterns)
		if blockedResult != nil || err != nil {
			return blockedResult, err
		}
	}
	return nil, nil
}

func validateSkillSQL(query string, patterns []*regexp.Regexp) (*tool.Result, error) {
	if err := CheckBlockedPatterns(query, patterns); err != nil {
		if errors.Is(err, ErrBlockedSQL) {
			return &tool.Result{
				Text: "(blocked: this query is not allowed by the active skill. Follow the skill instructions for the exact queries to run.)",
			}, nil
		}
		return nil, err
	}
	return nil, nil
}
