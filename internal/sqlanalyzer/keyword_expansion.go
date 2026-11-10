package sqlanalyzer

import "strings"

// ExpandedKeywordMatch records one matched danger keyword and its expanded surrounding identifier.
type ExpandedKeywordMatch struct {
	MatchedKeyword string
	Expanded       string
}

// ExpandMatchedKeywordMatches expands matched danger-keyword substrings into full surrounding SQL identifiers.
func ExpandMatchedKeywordMatches(sql string, matchedKeywords []string) []ExpandedKeywordMatch {
	lowerSQL := strings.ToLower(sql)
	var expanded []ExpandedKeywordMatch

	for _, keyword := range matchedKeywords {
		needle := strings.ToLower(strings.TrimSpace(keyword))
		if needle == "" {
			continue
		}

		searchFrom := 0
		for searchFrom < len(lowerSQL) {
			idx := strings.Index(lowerSQL[searchFrom:], needle)
			if idx < 0 {
				break
			}
			idx += searchFrom

			left := idx
			for left > 0 && isKeywordIdentifierChar(rune(lowerSQL[left-1])) {
				left--
			}

			right := idx + len(needle)
			for right < len(lowerSQL) && isKeywordIdentifierChar(rune(lowerSQL[right])) {
				right++
			}

			token := strings.TrimSpace(sql[left:right])
			if token != "" {
				expanded = append(expanded, ExpandedKeywordMatch{
					MatchedKeyword: keyword,
					Expanded:       token,
				})
			}

			searchFrom = idx + 1
		}
	}

	return expanded
}

// ExpandMatchedKeywords expands matched danger-keyword substrings into full surrounding SQL identifiers.
// For example, "create" inside "created_at" expands to "created_at".
func ExpandMatchedKeywords(sql string, matchedKeywords []string) []string {
	seen := make(map[string]struct{})
	var expanded []string

	for _, match := range ExpandMatchedKeywordMatches(sql, matchedKeywords) {
		key := strings.ToLower(match.Expanded)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			expanded = append(expanded, match.Expanded)
		}
	}

	return expanded
}

// UniqueExpandedKeywords returns unique expanded identifiers preserving first-seen order.
func UniqueExpandedKeywords(matches []ExpandedKeywordMatch) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, match := range matches {
		key := strings.ToLower(match.Expanded)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, match.Expanded)
	}
	return out
}

func isKeywordIdentifierChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_', r == '$', r == '#', r == '%':
		return true
	default:
		return false
	}
}
