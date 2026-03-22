package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JSONLStore is a file-backed Store implementation using JSONL format.
// Each session is stored as a separate .jsonl file.
// File names are derived from a hash of the sessionID to prevent
// path traversal and ensure safe filenames.
type JSONLStore struct {
	dir   string
	mu    sync.Mutex
	files map[string]*sessionFile
}

// NewJSONLStore creates a new JSONLStore.
// The workspace parameter is hashed into the directory structure
// to ensure different workspaces with identical session IDs
// do not share persisted history.
func NewJSONLStore(baseDir, workspace string) (*JSONLStore, error) {
	// Create workspace-scoped subdirectory.
	wsHash := safeFileName(workspace)
	dir := filepath.Join(baseDir, wsHash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create directory: %w", err)
	}
	return &JSONLStore{
		dir:   dir,
		files: make(map[string]*sessionFile),
	}, nil
}

// safeFileName produces a deterministic, filesystem-safe filename from
// a sessionID. This prevents path traversal via "../" in session IDs
// and avoids collisions across different workspaces.
func safeFileName(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(h[:16]) // 128-bit → 32 hex chars
}

func (s *JSONLStore) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, safeFileName(sessionID)+".jsonl")
}

func (s *JSONLStore) getFile(sessionID string) *sessionFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, ok := s.files[sessionID]
	if !ok {
		path := s.sessionPath(sessionID)
		sf = &sessionFile{
			path:     path,
			lockPath: path + ".lock",
		}
		s.files[sessionID] = sf
	}
	return sf
}

// Append persists a single entry to the session's JSONL file.
func (s *JSONLStore) Append(sessionID string, entry Entry) error {
	sf := s.getFile(sessionID)
	return sf.append(entry)
}

// List returns the most recent entries for a session.
// If limit <= 0, all entries are returned.
func (s *JSONLStore) List(sessionID string, limit int) ([]Entry, error) {
	sf := s.getFile(sessionID)
	entries, err := sf.readAll()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// Reset deletes all entries for a session.
func (s *JSONLStore) Reset(sessionID string) error {
	sf := s.getFile(sessionID)
	return sf.reset()
}

// AddAnchor adds an anchor entry to the session.
func (s *JSONLStore) AddAnchor(sessionID, name string, state map[string]any) error {
	return s.AddAnchorWithSummary(sessionID, name, "", state)
}

// AddAnchorWithSummary adds an anchor entry with an explicit summary.
func (s *JSONLStore) AddAnchorWithSummary(sessionID, name, summary string, state map[string]any) error {
	payload, err := safeMarshal(AnchorPayload{Name: name, Summary: summary, State: state})
	if err != nil {
		return fmt.Errorf("store: create anchor entry: %w", err)
	}
	return s.Append(sessionID, Entry{Kind: KindAnchor, Date: time.Now(), Payload: payload})
}

// Info returns summary statistics for a session.
func (s *JSONLStore) Info(sessionID string) (SessionInfo, error) {
	sf := s.getFile(sessionID)
	entries, err := sf.readAll()
	if err != nil {
		return SessionInfo{}, err
	}
	return summarizeEntries(sessionID, entries), nil
}

// ------------------------------------------------------------------ sessionFile

// sessionFile manages a single .jsonl file with incremental reads.
type sessionFile struct {
	mu         sync.Mutex
	path       string
	lockPath   string
	entries    []Entry
	readOffset int64
}

func (sf *sessionFile) readAll() ([]Entry, error) {
	lockFile, err := lockSessionFile(sf.lockPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlockSessionFile(lockFile) }()

	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.readLocked()
}

func (sf *sessionFile) readLocked() ([]Entry, error) {
	stat, err := os.Stat(sf.path)
	if os.IsNotExist(err) {
		sf.entries = nil
		sf.readOffset = 0
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: stat %s: %w", sf.path, err)
	}

	// If file was truncated, reset cache.
	if stat.Size() < sf.readOffset {
		sf.entries = nil
		sf.readOffset = 0
	}

	f, err := os.Open(sf.path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", sf.path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(sf.readOffset, 0); err != nil {
		return nil, fmt.Errorf("store: seek %s: %w", sf.path, err)
	}

	// Read new bytes from the offset.
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", sf.path, err)
	}

	if len(buf) > 0 {
		lines := strings.Split(string(buf), "\n")

		// Track how many bytes we successfully consumed.
		consumed := int64(0)
		for i, line := range lines {
			rawLen := int64(len(line) + 1) // +1 for the \n separator
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				consumed += rawLen
				continue
			}
			var entry Entry
			if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
				isLastLine := i == len(lines)-1
				if isLastLine {
					// Last line without trailing \n: could be an in-progress
					// write. Do NOT advance offset; it will be re-read next time.
					slog.Warn("store.read.partial_line", "path", sf.path, "error", err)
					break
				}
				// Middle line (terminated by \n): data is complete but corrupt.
				// Skip it and continue reading subsequent lines.
				slog.Warn("store.read.corrupt_line", "path", sf.path, "line", trimmed, "error", err)
				consumed += rawLen
				continue
			}
			sf.entries = append(sf.entries, entry)
			consumed += rawLen
		}
		sf.readOffset += consumed
	} else {
		// No new data; sync offset to current file size.
		sf.readOffset = stat.Size()
	}

	// Return a copy to avoid mutation.
	result := make([]Entry, len(sf.entries))
	copy(result, sf.entries)
	return result, nil
}

func (sf *sessionFile) append(entry Entry) error {
	lockFile, err := lockSessionFile(sf.lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = unlockSessionFile(lockFile) }()

	sf.mu.Lock()
	defer sf.mu.Unlock()

	// Sync cache before allocating ID.
	if _, err := sf.readLocked(); err != nil {
		return err
	}

	// Assign monotonic ID.
	nextID := int64(1)
	if len(sf.entries) > 0 {
		nextID = sf.entries[len(sf.entries)-1].ID + 1
	}
	entry.ID = nextID

	// Redact multimodal content before persistence.
	entry.Payload = redactPayload(entry.Payload)

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("store: marshal entry: %w", err)
	}

	f, err := os.OpenFile(sf.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("store: open %s for append: %w", sf.path, err)
	}
	defer func() { _ = f.Close() }()

	if err := sf.ensureTrailingNewline(f); err != nil {
		return fmt.Errorf("store: fix corrupted tail %s: %w", sf.path, err)
	}

	// Seek to end and write the entry.
	if _, err := f.Seek(0, 2); err != nil {
		return fmt.Errorf("store: seek %s: %w", sf.path, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("store: write %s: %w", sf.path, err)
	}

	// Update cache and offset.
	sf.entries = append(sf.entries, entry)
	offset, err := f.Seek(0, 2)
	if err != nil {
		return fmt.Errorf("store: seek %s: %w", sf.path, err)
	}
	sf.readOffset = offset

	return nil
}

// ensureTrailingNewline checks if the file ends with a newline.
// If not (crash mid-write), it appends one so subsequent writes
// start on a clean line.
func (sf *sessionFile) ensureTrailingNewline(f *os.File) error {
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return nil
	}
	tail := make([]byte, 1)
	if _, err := f.ReadAt(tail, stat.Size()-1); err == nil && tail[0] != '\n' {
		slog.Warn("store.append.corrupted_tail", "path", sf.path)
		if _, err := f.Seek(0, 2); err != nil {
			return err
		}
		_, err = f.Write([]byte{'\n'})
		return err
	}
	return nil
}

func (sf *sessionFile) reset() error {
	lockFile, err := lockSessionFile(sf.lockPath)
	if err != nil {
		return err
	}
	defer func() { _ = unlockSessionFile(lockFile) }()

	sf.mu.Lock()
	defer sf.mu.Unlock()

	if err := os.Remove(sf.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: remove %s: %w", sf.path, err)
	}
	sf.entries = nil
	sf.readOffset = 0
	return nil
}
