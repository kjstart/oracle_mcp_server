package sqlanalyzer

import (
	"reflect"
	"testing"
)

func TestExpandMatchedKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sql     string
		matched []string
		want    []string
	}{
		{
			name:    "underscore identifier with spaces",
			sql:     "select * from al_test_table where created_at < sysdate",
			matched: []string{"create"},
			want:    []string{"created_at"},
		},
		{
			name:    "function call argument",
			sql:     "select name,to_char(created_at,'yyyy-mm-dd') from al_test",
			matched: []string{"create"},
			want:    []string{"created_at"},
		},
		{
			name:    "select list column",
			sql:     "select name,created_at from al_test",
			matched: []string{"create"},
			want:    []string{"created_at"},
		},
		{
			name:    "deduplicate repeated matches",
			sql:     "select created_at, created_at from al_test",
			matched: []string{"create"},
			want:    []string{"created_at"},
		},
		{
			name:    "plain keyword remains plain keyword",
			sql:     "drop table demo",
			matched: []string{"drop"},
			want:    []string{"drop"},
		},
		{
			name:    "percent remains part of expanded word",
			sql:     "select '%UPDATE%' from dual",
			matched: []string{"update"},
			want:    []string{"%UPDATE%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExpandMatchedKeywords(tt.sql, tt.matched)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExpandMatchedKeywords(%q, %v) = %v, want %v", tt.sql, tt.matched, got, tt.want)
			}
		})
	}
}

func TestExpandMatchedKeywordMatches(t *testing.T) {
	t.Parallel()

	sql := "select created_at, drop_flag from demo where created_at < sysdate and drop = 1"
	got := ExpandMatchedKeywordMatches(sql, []string{"create", "drop"})
	want := []ExpandedKeywordMatch{
		{MatchedKeyword: "create", Expanded: "created_at"},
		{MatchedKeyword: "create", Expanded: "created_at"},
		{MatchedKeyword: "drop", Expanded: "drop_flag"},
		{MatchedKeyword: "drop", Expanded: "drop"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandMatchedKeywordMatches() = %#v, want %#v", got, want)
	}
}

func TestUniqueExpandedKeywords(t *testing.T) {
	t.Parallel()

	got := UniqueExpandedKeywords([]ExpandedKeywordMatch{
		{MatchedKeyword: "create", Expanded: "created_at"},
		{MatchedKeyword: "create", Expanded: "Created_At"},
		{MatchedKeyword: "drop", Expanded: "drop_flag"},
	})
	want := []string{"created_at", "drop_flag"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UniqueExpandedKeywords() = %#v, want %#v", got, want)
	}
}
