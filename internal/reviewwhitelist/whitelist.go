package reviewwhitelist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Entry stores whitelist data for one connection.
type Entry struct {
	Connection string   `json:"connection"`
	HeadLine   []string `json:"head_line"`
	Keyword    []string `json:"keyword:"`
	Prefix     []string `json:"prefix,omitempty"`
}

// Store manages whitelist.json persistence.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a whitelist store for the given file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultPath returns the whitelist.json path in the executable directory.
func DefaultPath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(exePath), "whitelist.json"), nil
}

// Path returns the underlying whitelist.json path.
func (s *Store) Path() string {
	return s.path
}

// ContainsHeadLine reports whether the connection + head_line pair already exists,
// using case-insensitive matching for the headline text.
func (s *Store) ContainsHeadLine(connection, headerLine string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Connection != connection {
			continue
		}
		for _, line := range entry.HeadLine {
			if strings.EqualFold(line, headerLine) {
				return true, nil
			}
		}
	}
	return false, nil
}

// AddHeadLine inserts the connection + head_line pair if it does not already exist,
// using case-insensitive matching to avoid duplicate entries that differ only by case.
func (s *Store) AddHeadLine(connection, headerLine string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return err
	}
	entry := findOrCreateEntry(&entries, connection)
	for _, line := range entry.HeadLine {
		if strings.EqualFold(line, headerLine) {
			return nil
		}
	}
	entry.HeadLine = append(entry.HeadLine, headerLine)
	return s.saveLocked(entries)
}

// AddKeyword inserts a keyword for the given connection if it does not already exist.
func (s *Store) AddKeyword(connection, keyword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return err
	}
	entry := findOrCreateEntry(&entries, connection)
	for _, existing := range entry.Keyword {
		if existing == keyword {
			return nil
		}
	}
	entry.Keyword = append(entry.Keyword, keyword)
	return s.saveLocked(entries)
}

// AddPrefix inserts a prefix for the given connection if it does not already exist (case-insensitive).
func (s *Store) AddPrefix(connection, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return err
	}
	entry := findOrCreateEntry(&entries, connection)
	for _, existing := range entry.Prefix {
		if strings.EqualFold(existing, prefix) {
			return nil
		}
	}
	entry.Prefix = append(entry.Prefix, prefix)
	return s.saveLocked(entries)
}

// ContainsMatchingPrefix reports whether any saved prefix for the connection matches
// the beginning of the normalized SQL (newlines removed, multiple spaces collapsed).
func (s *Store) ContainsMatchingPrefix(connection, sql string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	normalizedSQL := normalizeSQL(sql)
	for _, entry := range entries {
		if entry.Connection != connection {
			continue
		}
		for _, prefix := range entry.Prefix {
			if prefix == "" {
				continue
			}
			if strings.HasPrefix(normalizedSQL, normalizeSQL(prefix)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// normalizeSQL collapses newlines and multiple spaces into a single space for prefix matching.
func normalizeSQL(sql string) string {
	sql = strings.ReplaceAll(sql, "\r\n", " ")
	sql = strings.ReplaceAll(sql, "\r", " ")
	sql = strings.ReplaceAll(sql, "\n", " ")
	for strings.Contains(sql, "  ") {
		sql = strings.ReplaceAll(sql, "  ", " ")
	}
	return strings.TrimSpace(sql)
}

// ContainsKeywordCI reports whether the exact keyword exists for the given connection, case-insensitively.
func (s *Store) ContainsKeywordCI(connection, keyword string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Connection != connection {
			continue
		}
		for _, existing := range entry.Keyword {
			if strings.EqualFold(existing, keyword) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Store) loadLocked() ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("read whitelist file %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return []Entry{}, nil
	}

	var rawEntries []map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawEntries); err != nil {
		return nil, fmt.Errorf("parse whitelist file %s: %w", s.path, err)
	}

	entries := make([]Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		var connection string
		if msg, ok := raw["connection"]; ok {
			if err := json.Unmarshal(msg, &connection); err != nil {
				return nil, fmt.Errorf("parse whitelist connection in %s: %w", s.path, err)
			}
		}
		entry := findOrCreateEntry(&entries, connection)

		if msg, ok := raw["head_line"]; ok {
			var headLine []string
			if err := json.Unmarshal(msg, &headLine); err != nil {
				return nil, fmt.Errorf("parse whitelist head_line in %s: %w", s.path, err)
			}
			entry.HeadLine = appendUniqueStrings(entry.HeadLine, headLine...)
		}
		if msg, ok := raw["header_line"]; ok {
			var legacyHeader string
			if err := json.Unmarshal(msg, &legacyHeader); err != nil {
				return nil, fmt.Errorf("parse whitelist header_line in %s: %w", s.path, err)
			}
			entry.HeadLine = appendUniqueStrings(entry.HeadLine, legacyHeader)
		}
		if msg, ok := raw["keyword:"]; ok {
			var keywords []string
			if err := json.Unmarshal(msg, &keywords); err != nil {
				return nil, fmt.Errorf("parse whitelist keyword in %s: %w", s.path, err)
			}
			entry.Keyword = appendUniqueStrings(entry.Keyword, keywords...)
		}
		if msg, ok := raw["prefix"]; ok {
			var prefixes []string
			if err := json.Unmarshal(msg, &prefixes); err != nil {
				return nil, fmt.Errorf("parse whitelist prefix in %s: %w", s.path, err)
			}
			entry.Prefix = appendUniqueStrings(entry.Prefix, prefixes...)
		}
	}
	return entries, nil
}

func (s *Store) saveLocked(entries []Entry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal whitelist: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("create whitelist directory %s: %w", filepath.Dir(s.path), err)
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return fmt.Errorf("write whitelist file %s: %w", s.path, err)
	}
	return nil
}

func findOrCreateEntry(entries *[]Entry, connection string) *Entry {
	for i := range *entries {
		if (*entries)[i].Connection == connection {
			return &(*entries)[i]
		}
	}
	*entries = append(*entries, Entry{
		Connection: connection,
		HeadLine:   []string{},
		Keyword:    []string{},
	})
	return &(*entries)[len(*entries)-1]
}

func appendUniqueStrings(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}
