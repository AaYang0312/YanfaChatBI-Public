package main

import (
	"bytes"
	"testing"
)

func TestERPInstallRejectsDynamicExecution(t *testing.T) {
	for _, args := range [][]string{{"shell", "--input", "/missing"}, {"prepare", "--input", "/missing", "extra"}, {"verify", "--command", "echo"}} {
		var out, errout bytes.Buffer
		if execute(args, &out, &errout) == 0 || out.Len() != 0 {
			t.Fatal("invalid installer command succeeded")
		}
	}
}
