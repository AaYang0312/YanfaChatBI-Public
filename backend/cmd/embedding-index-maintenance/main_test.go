package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"yanfachatbi/pkg/embeddingmaintenance"
)

func inputFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../pkg/embeddingmaintenance/testdata/synthetic-plan-input.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func invoke(args []string, b []byte) (int, string, string) {
	var out, err bytes.Buffer
	code := run(args, bytes.NewReader(b), &out, &err)
	return code, out.String(), err.String()
}
func TestCLIPlanAndOfflinePreflight(t *testing.T) {
	input := inputFixture(t)
	code, out, stderr := invoke([]string{"plan"}, input)
	if code != 0 || stderr != "" {
		t.Fatalf("plan failed %d %s", code, stderr)
	}
	var plan embeddingmaintenance.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ID == "" || plan.LiveReady || plan.PauseAttested {
		t.Fatalf("invalid plan output %s", out)
	}
	var request struct {
		Inventory embeddingmaintenance.Inventory `json:"inventory"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"plan": plan, "inventory": request.Inventory, "now": request.Inventory.ObservedAt.Add(time.Minute)})
	code, out, stderr = invoke([]string{"preflight"}, b)
	var report embeddingmaintenance.Preflight
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr != "" || !report.MetadataConsistent || report.LiveReady || report.PauseAttested || report.OperationalRollbackReady {
		t.Fatalf("preflight %d %s %s", code, out, stderr)
	}
	b, _ = json.Marshal(map[string]any{"plan": plan, "scenario": embeddingmaintenance.Scenario{Rollback: true, ResumeWrites: true}})
	code, out, stderr = invoke([]string{"simulate"}, b)
	var sim embeddingmaintenance.Simulation
	if err := json.Unmarshal([]byte(out), &sim); err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr != "" || sim.Phase != "rollback-blocked" || !sim.NewMaintenanceRequired || sim.Selected != plan.Spec.Target {
		t.Fatalf("simulate %d %s %s", code, out, stderr)
	}
}

func TestCLIRejectsLiveActionsMalformedOrSecretFields(t *testing.T) {
	fixture := string(inputFixture(t))
	for _, tc := range []struct {
		name  string
		args  []string
		input string
	}{
		{"apply", []string{"apply"}, fixture},
		{"cutover", []string{"cutover"}, fixture},
		{"rebuild", []string{"rebuild-live"}, fixture},
		{"unknown", []string{"anything"}, fixture},
		{"extra args", []string{"plan", "--apply"}, fixture},
		{"empty", []string{"plan"}, ""},
		{"trailing", []string{"plan"}, fixture + " {}"},
		{"unknown field", []string{"plan"}, strings.Replace(fixture, "\"spec\": {", "\"spec\": {\"api_key\":\"not-a-real-secret\",", 1)},
		{"null", []string{"plan"}, "null"},
		{"oversize", []string{"plan"}, strings.Repeat(" ", 1024*1024+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := invoke(tc.args, []byte(tc.input))
			if code == 0 || out != "" || stderr == "" {
				t.Fatalf("accepted %d %q %q", code, out, stderr)
			}
			if strings.Contains(stderr, "not-a-real-secret") {
				t.Fatal("input value leaked")
			}
		})
	}
}
func TestCLIHelpAndSemanticRejection(t *testing.T) {
	code, out, stderr := invoke([]string{"--help"}, nil)
	if code != 0 || stderr != "" || !strings.Contains(out, "plan|preflight|simulate") || !strings.Contains(out, "No network") {
		t.Fatalf("help %d %s %s", code, out, stderr)
	}
	fixture := strings.Replace(string(inputFixture(t)), "qwen3.7-text-embedding-flash", "unsupported-model", 1)
	code, out, stderr = invoke([]string{"plan"}, []byte(fixture))
	if code == 0 || out != "" || !strings.Contains(stderr, "target must") {
		t.Fatalf("semantic validation %d %s %s", code, out, stderr)
	}
}

func TestCLIRejectionNeverEchoesSecretCanary(t *testing.T) {
	const canary = "SYNTHETIC_SECRET_CANARY_7x"
	fixture := string(inputFixture(t))
	for _, input := range []string{
		strings.Replace(fixture, "https://embedding.example.invalid/compatible-mode/v1", "https://"+canary+":password@example.invalid/compatible-mode/v1", 1),
		strings.Replace(fixture, "https://embedding.example.invalid/compatible-mode/v1", "https://example.invalid/compatible-mode/v1?key="+canary, 1),
		strings.Replace(fixture, "https://embedding.example.invalid/compatible-mode/v1", "https://%"+canary+"/compatible-mode/v1", 1),
		strings.Replace(fixture, "\"spec\": {", "\"spec\": {\""+canary+"\":\""+canary+"\",", 1),
		"{\"" + canary + "\": broken}",
	} {
		code, out, stderr := invoke([]string{"plan"}, []byte(input))
		if code == 0 {
			t.Fatal("unsafe input accepted")
		}
		if strings.Contains(out+stderr, canary) {
			t.Fatal("canary leaked")
		}
	}
}

func TestCLIRejectsMissingOrFalseExactCountEvidence(t *testing.T) {
	fixture := string(inputFixture(t))
	for _, input := range []string{
		strings.Replace(fixture, "\"points_count_exact\": true,", "", 1),
		strings.Replace(fixture, "\"points_count_exact\": true", "\"points_count_exact\": false", 1),
	} {
		code, out, stderr := invoke([]string{"plan"}, []byte(input))
		if code == 0 || out != "" || !strings.Contains(stderr, "exact point counts") {
			t.Fatalf("exact-count evidence not required: %d %s %s", code, out, stderr)
		}
	}
}
