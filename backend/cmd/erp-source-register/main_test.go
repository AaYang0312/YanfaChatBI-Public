package main

import (
	"bytes"
	"testing"
)

func TestERPRegistrationRejectsUnapprovedModes(t *testing.T) {
	for _, args := range [][]string{{"register", "--input", "/missing", "--output-dir", "/missing"}, {"stage-view", "--input", "/missing", "--source-id", "arbitrary"}, {"enable-sync", "--input", "/missing"}} {
		var out, errout bytes.Buffer
		if execute(args, &out, &errout) == 0 || out.Len() != 0 {
			t.Fatal("invalid registrar command mutated state")
		}
	}
}
