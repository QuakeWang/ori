package tool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var intPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// ParsedValue preserves both the normalized text and whether shell quoting
// was used. Command-mode type inference only applies to unquoted values.
type ParsedValue struct {
	Text   string
	Quoted bool
}

// ParsedArgs preserves positional and keyword command arguments.
type ParsedArgs struct {
	Positional []ParsedValue
	Keywords   map[string]ParsedValue
}

// ParseCommandDetailed parses a comma-prefixed command string into a command
// name plus positional/keyword arguments with quoting metadata retained.
//
// Rules (from GO_REWRITE_PLAN §7):
//   - leading "," is stripped
//   - first token is the command name (dotted external name)
//   - subsequent tokens are key=value pairs
//   - values can be single-quoted: key='value with spaces'
//   - positional arguments may not appear after keyword arguments
func ParseCommandDetailed(input string) (name string, args ParsedArgs, err error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, ",") {
		return "", ParsedArgs{}, fmt.Errorf("not a command: missing leading comma")
	}

	trimmed = strings.TrimSpace(trimmed[1:])
	if trimmed == "" {
		return "", ParsedArgs{}, fmt.Errorf("empty command")
	}

	tokens, err := tokenize(trimmed)
	if err != nil {
		return "", ParsedArgs{}, err
	}

	if len(tokens) == 0 {
		return "", ParsedArgs{}, fmt.Errorf("empty command")
	}

	name = tokens[0].Text
	args = ParsedArgs{
		Keywords: make(map[string]ParsedValue),
	}
	seenKeyword := false

	for _, tok := range tokens[1:] {
		if idx := strings.IndexByte(tok.Text, '='); idx > 0 {
			key := tok.Text[:idx]
			value := tok.Text[idx+1:]
			args.Keywords[key] = ParsedValue{
				Text:   value,
				Quoted: tok.Quoted,
			}
			seenKeyword = true
		} else {
			if seenKeyword {
				return "", ParsedArgs{}, fmt.Errorf("positional argument %q after keyword argument", tok.Text)
			}
			args.Positional = append(args.Positional, tok)
		}
	}

	return name, args, nil
}

// CommandJSONArgs converts parsed command arguments into a JSON-compatible
// object. Positional arguments use _0/_1 keys for compatibility.
func CommandJSONArgs(args ParsedArgs) map[string]any {
	result := make(map[string]any, len(args.Keywords)+len(args.Positional))
	for idx, value := range args.Positional {
		result[fmt.Sprintf("_%d", idx)] = value.Text
	}
	for key, value := range args.Keywords {
		result[key] = inferCommandValue(value)
	}
	return result
}

func inferCommandValue(value ParsedValue) any {
	if value.Quoted {
		return value.Text
	}

	switch strings.ToLower(value.Text) {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}

	if intPattern.MatchString(value.Text) {
		if n, err := strconv.Atoi(value.Text); err == nil {
			return n
		}
	}

	if strings.ContainsAny(value.Text, ".eE") {
		if n, err := strconv.ParseFloat(value.Text, 64); err == nil {
			return n
		}
	}

	return value.Text
}

// tokenize splits a string by shell-like whitespace rules while retaining
// whether a token used quotes anywhere in its original form.
func tokenize(s string) ([]ParsedValue, error) {
	var tokens []ParsedValue
	var current strings.Builder
	inQuote := byte(0)
	currentQuoted := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			current.WriteByte(ch)
			escaped = false
		case ch == '\\' && inQuote != '\'':
			escaped = true
		case (ch == '\'' || ch == '"') && inQuote == 0:
			inQuote = ch
			currentQuoted = true
		case ch == inQuote:
			inQuote = 0
		case (ch == ' ' || ch == '\t' || ch == '\n') && inQuote == 0:
			if current.Len() > 0 || currentQuoted {
				tokens = append(tokens, ParsedValue{
					Text:   current.String(),
					Quoted: currentQuoted,
				})
				current.Reset()
				currentQuoted = false
			}
		default:
			current.WriteByte(ch)
		}
	}

	if escaped {
		current.WriteByte('\\')
	}
	if inQuote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}

	if current.Len() > 0 || currentQuoted {
		tokens = append(tokens, ParsedValue{
			Text:   current.String(),
			Quoted: currentQuoted,
		})
	}

	return tokens, nil
}
