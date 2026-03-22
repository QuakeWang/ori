package doris

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
)

var (
	allowedPrefixes = regexp.MustCompile(
		`(?i)^\s*(SELECT|SHOW|DESCRIBE|DESC|EXPLAIN|ADMIN\s+SHOW|ADMIN\s+DIAGNOSE|HELP)\b`,
	)
	blockedPrefixes = regexp.MustCompile(`(?i)^\s*ADMIN\s+(SET|REPAIR|CANCEL|COMPACT|CHECK)\b`)
	setPrefix       = regexp.MustCompile(`(?i)^\s*SET\s+`)
	setScopePrefix  = regexp.MustCompile(`(?i)^(GLOBAL|DEFAULT|LOCAL)\b`)
	setTargetRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	whitespaceRE    = regexp.MustCompile(`\s+`)

	// ErrBlockedSQL reports that a query is disallowed by the active skill.
	ErrBlockedSQL = errors.New("doris query blocked by active skill")
)

// IsReadOnly reports whether a SQL statement is allowed by the Phase 2 safety policy.
func IsReadOnly(query string) bool {
	trimmed := trimSQL(query)
	if trimmed == "" {
		return false
	}
	if blockedPrefixes.MatchString(trimmed) {
		return false
	}
	return allowedPrefixes.MatchString(trimmed)
}

// IsSetStatement reports whether the SQL is a single session-local SET
// assignment suitable for doris.session setup_sqls.
func IsSetStatement(query string) bool {
	_, ok := parseSessionSetTarget(query)
	return ok
}

func parseSessionSetTarget(query string) (string, bool) {
	trimmed := trimSQL(query)
	if trimmed == "" || !setPrefix.MatchString(trimmed) || strings.Contains(trimmed, ";") {
		return "", false
	}

	rest := strings.TrimSpace(trimmed[len("SET"):])
	if rest == "" || setScopePrefix.MatchString(rest) {
		return "", false
	}

	upperRest := strings.ToUpper(rest)
	if strings.HasPrefix(upperRest, "SESSION ") {
		rest = strings.TrimSpace(rest[len("SESSION"):])
		upperRest = strings.ToUpper(rest)
	}

	// Keep the tool contract narrow: one session variable assignment per setup SQL.
	if strings.Contains(rest, ",") {
		return "", false
	}

	if strings.HasPrefix(upperRest, "TRANSACTION ") ||
		strings.HasPrefix(upperRest, "NAMES ") ||
		strings.HasPrefix(upperRest, "CHARSET ") ||
		strings.HasPrefix(upperRest, "CHARACTER SET ") ||
		strings.HasPrefix(upperRest, "PASSWORD ") {
		return "", false
	}

	eq := strings.Index(rest, "=")
	if eq <= 0 || eq == len(rest)-1 {
		return "", false
	}

	target := strings.TrimSpace(rest[:eq])
	value := strings.TrimSpace(rest[eq+1:])
	if target == "" || value == "" {
		return "", false
	}

	if strings.HasPrefix(target, "@@") {
		upperTarget := strings.ToUpper(target)
		if !strings.HasPrefix(upperTarget, "@@SESSION.") {
			return "", false
		}
		target = target[len("@@SESSION."):]
	}

	if strings.HasPrefix(target, "@") || !setTargetRE.MatchString(target) {
		return "", false
	}
	return target, true
}

// NormalizeSQL canonicalizes whitespace and uppercases the query for policy checks.
func NormalizeSQL(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ""
	}
	return strings.ToUpper(whitespaceRE.ReplaceAllString(trimmed, " "))
}

// BlockedPatternsFromState compiles blocked_sql patterns from the runtime
// policy selected by the latest explicit skill activation.
func BlockedPatternsFromState(state *session.State) ([]*regexp.Regexp, error) {
	raw := skill.ActiveBlockedSQL(state)
	if len(raw) == 0 {
		return nil, nil
	}

	values, err := decodeBlockedSQL(raw)
	if err != nil {
		return nil, fmt.Errorf("decode active blocked_sql: %w", err)
	}

	patterns := make([]*regexp.Regexp, 0, len(values))
	for _, value := range values {
		pattern, err := regexp.Compile(value)
		if err != nil {
			return nil, fmt.Errorf("compile active blocked_sql: %w", err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// CheckBlockedPatterns validates the query against skill-driven SQL restrictions.
func CheckBlockedPatterns(query string, patterns []*regexp.Regexp) error {
	if len(patterns) == 0 {
		return nil
	}

	normalized := NormalizeSQL(query)
	for _, pattern := range patterns {
		if pattern.MatchString(normalized) {
			return fmt.Errorf("%w: %s", ErrBlockedSQL, pattern.String())
		}
	}
	return nil
}

func decodeBlockedSQL(raw json.RawMessage) ([]string, error) {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}

	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, nil
		}
		return []string{single}, nil
	}

	return nil, fmt.Errorf("expected string or []string")
}

func trimSQL(query string) string {
	trimmed := strings.TrimSpace(query)
	trimmed = strings.TrimRight(trimmed, ";")
	return strings.TrimSpace(trimmed)
}
