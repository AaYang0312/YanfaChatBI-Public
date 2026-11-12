package services

import (
	"fmt"
	"regexp"
	"strings"
)

var newChatTableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$`)

// newChatSelection validates syntax only. It never grants source/reader access
// or discovers tables (which could trigger an unscoped read before persistence).
// Use the table keys returned by the driver, not SQL identifier quoting or views.
func newChatSelection(input *string, fallback string) (string, error) {
	if input == nil {
		return fallback, nil
	}
	value := strings.TrimSpace(*input)
	if value == "ALL" {
		return value, nil
	}
	names := strings.Split(value, ",")
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "ALL" || !newChatTableName.MatchString(name) || seen[name] {
			return "", fmt.Errorf("selected_collections must be ALL or a nonempty comma-separated list of distinct table names (optionally schema-qualified)")
		}
		seen[name] = true
		names[i] = name
	}
	return strings.Join(names, ","), nil
}
