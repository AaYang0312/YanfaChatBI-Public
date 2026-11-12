package dbmanager

import (
	"regexp"
	"strings"
)

var writeKeywordPattern = regexp.MustCompile(`\b(INSERT|UPDATE|DELETE|MERGE|CREATE|DROP|ALTER|TRUNCATE|GRANT|REVOKE|UPSERT|REPLACE)\b`)

// isRowReturningStatement reports whether a SQL statement returns rows and
// must run through QueryContext. It recognises CTEs whose body is a write
// (`WITH ... DELETE ...`) by rejecting any write keyword outside the leading
// command, so a read-only CTE is not executed as a command and silently
// converted into "rows affected".
func isRowReturningStatement(stmt string) bool {
	trimmed := strings.TrimSpace(stmt)
	trimmed = stripLeadingSQLComments(trimmed)
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "SELECT"),
		strings.HasPrefix(upper, "SHOW"),
		strings.HasPrefix(upper, "EXPLAIN"),
		strings.HasPrefix(upper, "VALUES"),
		strings.HasPrefix(upper, "TABLE"):
		return true
	case strings.HasPrefix(upper, "WITH"):
		return !writeKeywordPattern.MatchString(upper)
	}
	return false
}

// stripLeadingSQLComments removes leading -- line comments and /* */ blocks.
func stripLeadingSQLComments(stmt string) string {
	rest := strings.TrimSpace(stmt)
	for {
		switch {
		case strings.HasPrefix(rest, "--"):
			if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
				rest = strings.TrimSpace(rest[idx+1:])
				continue
			}
			return ""
		case strings.HasPrefix(rest, "/*"):
			if idx := strings.Index(rest, "*/"); idx >= 0 {
				rest = strings.TrimSpace(rest[idx+2:])
				continue
			}
			return ""
		default:
			return rest
		}
	}
}
