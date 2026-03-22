package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuakeWang/ori/internal/tool"
)

const (
	// maxReadSize is the maximum file size (in bytes) that fs.read / fs.edit
	// will accept. Prevents reading unreasonably large files. With streaming
	// pagination, this can be set relatively high since we never load the
	// entire file into memory for reads.
	maxReadSize = 10 << 20 // 10 MiB

	// maxEditSize is the maximum file size for fs.edit, which still needs to
	// load the entire file for in-place replacement.
	maxEditSize = 1 << 20 // 1 MiB

	// defaultReadLimit is the maximum number of lines returned when the
	// caller does not specify a limit. Prevents flooding the LLM context.
	defaultReadLimit = 200

	// maxResultBytes is the hard limit on the byte size of the returned text.
	// This prevents a single long line or large explicit limit from blowing
	// up the context window. 64 KiB is roughly 16K tokens.
	maxResultBytes = 64 << 10 // 64 KiB
)

type fsReadInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

type fsWriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fsEditInput struct {
	Path  string `json:"path"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Start int    `json:"start,omitempty"`
}

func fsReadTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "fs.read",
			Description: "Read a text file and return its content. Supports optional pagination with offset and limit (line-based).",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path (absolute or relative to workspace)"},"offset":{"type":"integer","description":"Start line (0-indexed)"},"limit":{"type":"integer","description":"Max lines to return"}},"required":["path"]}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in fsReadInput
			if err := decodeToolInput(input, &in, "fs.read"); err != nil {
				return nil, err
			}

			resolved, err := resolveReadablePath(tc.Workspace, in.Path, maxReadSize)
			if err != nil {
				return nil, err
			}

			limit := defaultReadLimit
			if in.Limit != nil {
				limit = max(0, *in.Limit)
			}
			offset := max(0, in.Offset)

			text, linesRead, totalLines, truncation, err := readLines(resolved, offset, limit)
			if err != nil {
				return nil, err
			}

			result := &tool.Result{
				Text:      text,
				Truncated: truncation != "",
			}
			if truncation != "" {
				result.Text += fmt.Sprintf("\n\n[truncated: %s; showing %d of %d total lines; use offset/limit to paginate]",
					truncation, linesRead, totalLines)
			}
			return result, nil
		},
	}
}

func fsWriteTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "fs.write",
			Description: "Write content to a text file.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in fsWriteInput
			if err := decodeToolInput(input, &in, "fs.write"); err != nil {
				return nil, err
			}

			resolved, err := resolvePath(tc.Workspace, in.Path)
			if err != nil {
				return nil, err
			}

			if err := ensureParentDir(resolved); err != nil {
				return nil, err
			}
			if err := writeTextFile(resolved, []byte(in.Content)); err != nil {
				return nil, err
			}

			return &tool.Result{Text: fmt.Sprintf("wrote: %s", resolved)}, nil
		},
	}
}

func fsEditTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "fs.edit",
			Description: "Edit a text file by replacing old text with new text. Optionally specify the start line.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"old":{"type":"string","description":"Text to find"},"new":{"type":"string","description":"Replacement text"},"start":{"type":"integer","description":"Start line for search (0-indexed)"}},"required":["path","old","new"]}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in fsEditInput
			if err := decodeToolInput(input, &in, "fs.edit"); err != nil {
				return nil, err
			}

			resolved, data, err := readEditableFile(tc.Workspace, in.Path)
			if err != nil {
				return nil, err
			}

			result, err := replaceFromLine(string(data), in.Old, in.New, in.Start, resolved)
			if err != nil {
				return nil, err
			}

			if err := writeTextFile(resolved, []byte(result)); err != nil {
				return nil, err
			}

			return &tool.Result{Text: fmt.Sprintf("edited: %s", resolved)}, nil
		},
	}
}

// resolvePath resolves a raw path against the workspace and enforces
// workspace boundary: the real path (after symlink resolution) must be
// under the workspace root. This prevents both direct and symlink-based
// escape from the workspace.
func resolvePath(workspace, rawPath string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("path %q is not allowed without a workspace", rawPath)
	}

	// Expand ~ prefix.
	if strings.HasPrefix(rawPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~: %w", err)
		}
		rawPath = filepath.Join(home, rawPath[2:])
	}

	var resolved string
	if filepath.IsAbs(rawPath) {
		resolved = filepath.Clean(rawPath)
	} else {
		resolved = filepath.Clean(filepath.Join(workspace, rawPath))
	}

	// Resolve the real workspace path (following symlinks).
	workspaceReal, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace: %w", err)
	}
	workspaceReal, err = filepath.Abs(workspaceReal)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace: %w", err)
	}

	// Resolve the real target path following symlinks.
	// The target may not exist yet, so we walk up to find the closest
	// existing ancestor, resolve its symlinks, then rejoin the tail.
	resolvedReal, err := evalSymlinksWalkUp(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", rawPath, err)
	}
	resolvedReal, err = filepath.Abs(resolvedReal)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}

	// The real path must be under or equal to the real workspace.
	if !strings.HasPrefix(resolvedReal, workspaceReal+string(filepath.Separator)) && resolvedReal != workspaceReal {
		return "", fmt.Errorf("path %q escapes workspace boundary %q", rawPath, workspace)
	}

	return resolved, nil
}

// checkReadable validates that a file is suitable for reading: it must exist,
// be a regular file, and not exceed the given size limit.
func checkReadable(path string, sizeLimit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > sizeLimit {
		return fmt.Errorf("%s is too large (%d bytes, max %d)", path, info.Size(), sizeLimit)
	}
	return nil
}

func resolveReadablePath(workspace, rawPath string, sizeLimit int64) (string, error) {
	resolved, err := resolvePath(workspace, rawPath)
	if err != nil {
		return "", err
	}
	if err := checkReadable(resolved, sizeLimit); err != nil {
		return "", err
	}
	return resolved, nil
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create directory %s: %w", dir, err)
	}
	return nil
}

func writeTextFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

func readEditableFile(workspace, rawPath string) (string, []byte, error) {
	resolved, err := resolveReadablePath(workspace, rawPath, maxEditSize)
	if err != nil {
		return "", nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("cannot read %s: %w", resolved, err)
	}
	if isBinary(data) {
		return "", nil, fmt.Errorf("%s appears to be a binary file", resolved)
	}
	return resolved, data, nil
}

// isBinary returns true if the first 512 bytes of data contain a NUL byte,
// which is a strong indicator of binary content.
func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return false
}

// readLines performs streaming line-based pagination on a file.
// It reads only the lines needed (offset..offset+limit) via bufio.Scanner,
// never loading the entire file into memory. It also enforces maxResultBytes.
//
// Returns:
//   - text: the joined lines
//   - linesRead: number of lines actually included
//   - totalLines: total line count in the file (-1 if unknown / early exit)
//   - truncation: empty string if not truncated, or a reason string
//   - err: any I/O or binary detection error
func readLines(path string, offset, limit int) (text string, linesRead, totalLines int, truncation string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Allow lines up to 1 MiB to handle long single-line files gracefully.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var (
		collected []string
		lineNo    int
		byteCount int
		byteCap   bool
		binaryBuf []byte // first 512 bytes for binary detection
	)

	for scanner.Scan() {
		line := scanner.Text()

		// Binary detection: accumulate first 512 bytes.
		if len(binaryBuf) < 512 {
			remaining := 512 - len(binaryBuf)
			raw := scanner.Bytes()
			if len(raw) < remaining {
				binaryBuf = append(binaryBuf, raw...)
			} else {
				binaryBuf = append(binaryBuf, raw[:remaining]...)
			}
			if isBinary(binaryBuf) {
				return "", 0, 0, "", fmt.Errorf("%s appears to be a binary file", path)
			}
		}

		if lineNo < offset {
			lineNo++
			continue
		}

		if linesRead >= limit {
			// We've collected enough lines; keep scanning to count total.
			lineNo++
			continue
		}

		lineBytes := len(line)
		if byteCount+lineBytes+1 > maxResultBytes { // +1 for newline
			byteCap = true
			// Don't add this line; keep scanning to count total.
			lineNo++
			continue
		}

		collected = append(collected, line)
		byteCount += lineBytes + 1
		linesRead++
		lineNo++
	}

	if err := scanner.Err(); err != nil {
		return "", 0, 0, "", fmt.Errorf("cannot read %s: %w", path, err)
	}

	totalLines = lineNo
	text = strings.Join(collected, "\n")

	// Determine truncation reason.
	if byteCap {
		truncation = fmt.Sprintf("byte limit reached (%d bytes max)", maxResultBytes)
	} else if isDefaultLimitReached(limit, linesRead, totalLines-offset) {
		truncation = fmt.Sprintf("default limit (%d lines)", defaultReadLimit)
	}

	return text, linesRead, totalLines, truncation, nil
}

// isDefaultLimitReached returns true when the limit appears to have come from
// the default and we actually hit it (i.e., there were more lines available).
func isDefaultLimitReached(limit, linesRead, available int) bool {
	return limit == defaultReadLimit && linesRead >= limit && available > limit
}

func replaceFromLine(content, old, new string, start int, path string) (string, error) {
	requestedStart := start
	lines := strings.Split(content, "\n")
	start = max(0, min(start, len(lines)))

	before := strings.Join(lines[:start], "\n")
	after := strings.Join(lines[start:], "\n")
	if !strings.Contains(after, old) {
		return "", fmt.Errorf("'%s' not found in %s from line %d", old, path, requestedStart)
	}

	replaced := strings.Replace(after, old, new, 1)
	if before == "" {
		return replaced, nil
	}
	return before + "\n" + replaced, nil
}

// evalSymlinksWalkUp resolves symlinks by walking up the path until an
// existing ancestor is found, then rejoining the non-existent tail.
func evalSymlinksWalkUp(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}

	// Walk up to find the closest existing ancestor.
	parent := filepath.Dir(path)
	if parent == path {
		// Reached root without finding existing path.
		return path, nil
	}

	parentReal, err := evalSymlinksWalkUp(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parentReal, filepath.Base(path)), nil
}
