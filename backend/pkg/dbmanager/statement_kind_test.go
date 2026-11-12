package dbmanager

import "testing"

func TestIsRowReturningStatement(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"  select * from t", true},
		{"WITH q AS (SELECT 1 AS a) SELECT a FROM q", true},
		{"WITH q AS (SELECT 1) UPDATE t SET x = 1 FROM q", false},
		{"WITH d AS (DELETE FROM t RETURNING id) SELECT id FROM d", false},
		{"-- comment\nWITH q AS (SELECT 1) SELECT * FROM q", true},
		{"/* block */ SELECT 1", true},
		{"INSERT INTO t VALUES (1)", false},
		{"SHOW TABLES", true},
		{"EXPLAIN SELECT 1", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := isRowReturningStatement(tc.sql); got != tc.want {
			t.Fatalf("isRowReturningStatement(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}
