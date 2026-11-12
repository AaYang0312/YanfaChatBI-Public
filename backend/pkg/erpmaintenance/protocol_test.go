package erpmaintenance

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMaintenanceClosedFrames(t *testing.T) {
	r := Request{Protocol: Protocol, SourceID: uuid.NewString(), RequestID: uuid.NewString(), Operation: "incremental"}
	var valid bytes.Buffer
	if Encode(&valid, r) != nil {
		t.Fatal("encode failed")
	}
	frame := valid.String()
	var decoded Request
	if Decode(strings.NewReader(frame), &decoded) != nil || decoded != r {
		t.Fatal("valid request rejected")
	}
	for _, raw := range []string{strings.Replace(frame, "\"operation\":", "\"OPERATION\":", 1), strings.Replace(frame, "\"operation\":", "\"operation\":\"incremental\",\"operation\":", 1), strings.TrimSuffix(frame, "\n"), frame + frame, strings.Replace(frame, "incremental", "backfill", 1), strings.Repeat("x", MaxFrame+1) + "\n"} {
		var req Request
		if Decode(strings.NewReader(raw), &req) == nil {
			t.Fatal("ambiguous or unauthorized request accepted")
		}
	}
}
