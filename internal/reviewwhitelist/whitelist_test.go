package reviewwhitelist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAddAndContainsHeadLine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whitelist.json")
	store := NewStore(path)

	found, err := store.ContainsHeadLine("play", "create or replace procedure demo")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error: %v", err)
	}
	if found {
		t.Fatal("ContainsHeadLine unexpectedly returned true before AddHeadLine")
	}

	if err := store.AddHeadLine("play", "create or replace procedure demo"); err != nil {
		t.Fatalf("AddHeadLine returned error: %v", err)
	}
	if err := store.AddHeadLine("play", "create or replace procedure demo"); err != nil {
		t.Fatalf("AddHeadLine duplicate returned error: %v", err)
	}
	if err := store.AddKeyword("play", "created_at"); err != nil {
		t.Fatalf("AddKeyword returned error: %v", err)
	}
	found, err = store.ContainsKeywordCI("play", "CREATED_AT")
	if err != nil {
		t.Fatalf("ContainsKeywordCI returned error: %v", err)
	}
	if !found {
		t.Fatal("ContainsKeywordCI did not match case-insensitively")
	}

	found, err = store.ContainsHeadLine("play", "create or replace procedure demo")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error after AddHeadLine: %v", err)
	}
	if !found {
		t.Fatal("ContainsHeadLine returned false after AddHeadLine")
	}

	found, err = store.ContainsHeadLine("play", "CREATE OR REPLACE PROCEDURE DEMO")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error for uppercase variant: %v", err)
	}
	if !found {
		t.Fatal("ContainsHeadLine did not match case-insensitively")
	}

	found, err = store.ContainsHeadLine("play", "create or replace procedure other_demo")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error for different header: %v", err)
	}
	if found {
		t.Fatal("ContainsHeadLine unexpectedly matched a different header")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	want := "[\n  {\n    \"connection\": \"play\",\n    \"head_line\": [\n      \"create or replace procedure demo\"\n    ],\n    \"keyword:\": [\n      \"created_at\"\n    ]\n  }\n]\n"
	if string(data) != want {
		t.Fatalf("unexpected whitelist content:\n%s", string(data))
	}
}

func TestStoreAddHeadLineSkipsCaseOnlyDuplicates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whitelist.json")
	store := NewStore(path)

	if err := store.AddHeadLine("play", "create or replace procedure demo"); err != nil {
		t.Fatalf("AddHeadLine returned error: %v", err)
	}
	if err := store.AddHeadLine("play", "CREATE OR REPLACE PROCEDURE DEMO"); err != nil {
		t.Fatalf("AddHeadLine uppercase duplicate returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	want := "[\n  {\n    \"connection\": \"play\",\n    \"head_line\": [\n      \"create or replace procedure demo\"\n    ],\n    \"keyword:\": []\n  }\n]\n"
	if string(data) != want {
		t.Fatalf("unexpected whitelist content after case-only duplicate:\n%s", string(data))
	}
}

func TestStoreLoadsLegacyHeaderLineFormat(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whitelist.json")
	legacy := "[\n  {\n    \"connection\": \"play\",\n    \"header_line\": \"CREATE OR REPLACE PROCEDURE al_test IS\"\n  }\n]\n"
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	store := NewStore(path)
	found, err := store.ContainsHeadLine("play", "CREATE OR REPLACE PROCEDURE al_test IS")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error: %v", err)
	}
	if !found {
		t.Fatal("ContainsHeadLine did not match migrated legacy content")
	}

	found, err = store.ContainsHeadLine("play", "create or replace procedure AL_TEST is")
	if err != nil {
		t.Fatalf("ContainsHeadLine returned error for mixed-case migrated content: %v", err)
	}
	if !found {
		t.Fatal("ContainsHeadLine did not match migrated content case-insensitively")
	}

	if err := store.AddKeyword("play", "created_at"); err != nil {
		t.Fatalf("AddKeyword returned error: %v", err)
	}
	found, err = store.ContainsKeywordCI("play", "CREATED_AT")
	if err != nil {
		t.Fatalf("ContainsKeywordCI returned error: %v", err)
	}
	if !found {
		t.Fatal("ContainsKeywordCI did not match migrated content case-insensitively")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	want := "[\n  {\n    \"connection\": \"play\",\n    \"head_line\": [\n      \"CREATE OR REPLACE PROCEDURE al_test IS\"\n    ],\n    \"keyword:\": [\n      \"created_at\"\n    ]\n  }\n]\n"
	if string(data) != want {
		t.Fatalf("unexpected migrated whitelist content:\n%s", string(data))
	}
}

func TestStoreCreatesMissingParentDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing", "nested", "whitelist.json")
	store := NewStore(path)

	if err := store.AddHeadLine("play", "CREATE TABLE demo (id NUMBER)"); err != nil {
		t.Fatalf("AddHeadLine returned error for missing parent directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("whitelist file was not created: %v", err)
	}
}

func TestAddAndContainsMatchingPrefix(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whitelist.json")
	store := NewStore(path)

	found, err := store.ContainsMatchingPrefix("play", "SELECT * FROM employees")
	if err != nil {
		t.Fatalf("ContainsMatchingPrefix returned error: %v", err)
	}
	if found {
		t.Fatal("ContainsMatchingPrefix unexpectedly returned true before AddPrefix")
	}

	if err := store.AddPrefix("play", "SELECT * FROM"); err != nil {
		t.Fatalf("AddPrefix returned error: %v", err)
	}
	if err := store.AddPrefix("play", "SELECT * FROM"); err != nil {
		t.Fatalf("AddPrefix duplicate returned error: %v", err)
	}

	// Exact prefix match with normalization
	found, err = store.ContainsMatchingPrefix("play", "SELECT * FROM employees")
	if err != nil {
		t.Fatalf("ContainsMatchingPrefix returned error: %v", err)
	}
	if !found {
		t.Fatal("ContainsMatchingPrefix should match SQL starting with saved prefix")
	}

	// Newlines and extra spaces in SQL should be normalized
	found, err = store.ContainsMatchingPrefix("play", "SELECT  *\nFROM employees")
	if err != nil {
		t.Fatalf("ContainsMatchingPrefix returned error for normalized sql: %v", err)
	}
	if !found {
		t.Fatal("ContainsMatchingPrefix should match after normalizing newlines/spaces")
	}

	// Different connection should not match
	found, err = store.ContainsMatchingPrefix("test", "SELECT * FROM employees")
	if err != nil {
		t.Fatalf("ContainsMatchingPrefix returned error for different connection: %v", err)
	}
	if found {
		t.Fatal("ContainsMatchingPrefix should not match a different connection")
	}

	// SQL that does not start with prefix should not match
	found, err = store.ContainsMatchingPrefix("play", "DELETE FROM employees")
	if err != nil {
		t.Fatalf("ContainsMatchingPrefix returned error for non-matching sql: %v", err)
	}
	if found {
		t.Fatal("ContainsMatchingPrefix should not match SQL with different prefix")
	}
}

func TestNormalizeSQL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"SELECT  *  FROM  t", "SELECT * FROM t"},
		{"SELECT\n*\nFROM t", "SELECT * FROM t"},
		{"SELECT\r\n*\r\nFROM t", "SELECT * FROM t"},
		{"  SELECT * FROM t  ", "SELECT * FROM t"},
	}
	for _, tc := range cases {
		got := normalizeSQL(tc.input)
		if got != tc.want {
			t.Errorf("normalizeSQL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestContainsKeywordCIHonorsConnection(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "whitelist.json")
	store := NewStore(path)
	if err := store.AddKeyword("play", "created_at"); err != nil {
		t.Fatalf("AddKeyword returned error: %v", err)
	}

	found, err := store.ContainsKeywordCI("test", "created_at")
	if err != nil {
		t.Fatalf("ContainsKeywordCI returned error: %v", err)
	}
	if found {
		t.Fatal("ContainsKeywordCI unexpectedly matched a different connection")
	}
}
