package main

import (
	"bytes"
	"testing"
)

func TestMaintenanceRejectsArbitraryCommands(t *testing.T) {
	for _, args := range [][]string{{"--source-id", "not-a-source", "incremental"}, {"--source-id", "00000000-0000-4000-8000-000000000001", "backfill"}, {"--source-id", "00000000-0000-4000-8000-000000000001", "incremental", "extra"}} {
		var out, errout bytes.Buffer
		if execute(args, &out, &errout) != 2 || out.Len() != 0 {
			t.Fatal("invalid command reached backend")
		}
	}
}
