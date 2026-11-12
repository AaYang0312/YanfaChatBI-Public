package embeddingmaintenance

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func syntheticInput() (Spec, Inventory) {
	zero := uint64(0)
	return Spec{Provider: "openai", Model: "qwen3.7-text-embedding-flash", Endpoint: "https://embedding.example.invalid/compatible-mode/v1", Dimensions: 1024, Distance: "Cosine", Old: Pair{"old_schema", "old_messages"}, Target: Pair{"new_schema", "new_messages"}, MaxInventoryAgeSeconds: 300}, Inventory{InstanceID: "synthetic-instance", ObservedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), Provenance: "invented unit-test inventory, not a server audit", CollectionsComplete: true, AliasesComplete: true, Collections: []Collection{{Name: "old_schema", VectorType: "dense", Dimensions: 768, Distance: "Cosine", PointsCount: &zero, PointsCountExact: true, Status: "green"}, {Name: "old_messages", VectorType: "dense", Dimensions: 768, Distance: "Cosine", PointsCount: &zero, PointsCountExact: true, Status: "green"}}, Aliases: []Alias{}}
}

func TestPlanEmptyBootstrapDoesNotClaimHistoricalOrOperationalReadiness(t *testing.T) {
	spec, inv := syntheticInput()
	p, err := PlanBootstrap(spec, inv)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Kind != "empty-index-bootstrap" {
		t.Fatalf("missing bound plan: %+v", p)
	}
	if p.LiveReady || p.PauseAttested || p.OperationalRollbackReady || p.HistoricalCoverage != "unknown" {
		t.Fatalf("unproved readiness: %+v", p)
	}
	if len(p.PendingPrerequisites) < 4 || !strings.Contains(strings.Join(p.PendingPrerequisites, " "), "old provider/model") {
		t.Fatalf("missing prerequisite: %+v", p)
	}
	if p.Estimate.Requests != nil || p.Estimate.DurationKnown {
		t.Fatalf("unknown source cannot produce an ETA: %+v", p.Estimate)
	}
	// Caller mutation must not change the inventory frozen in the plan.
	inv.Collections[0].Dimensions = 123
	*inv.Collections[0].PointsCount = 9
	if p.Baseline.Collections[0].Dimensions != 768 || *p.Baseline.Collections[0].PointsCount != 0 {
		t.Fatal("plan aliases caller memory")
	}
}

func TestPlanRejectsUnsafeOrUnknownMetadata(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Spec, *Inventory)
	}{
		{"provider", func(s *Spec, i *Inventory) { s.Provider = "gemini" }},
		{"model", func(s *Spec, i *Inventory) { s.Model = "qwen3.7-text-embedding" }},
		{"dimensions", func(s *Spec, i *Inventory) { s.Dimensions = 768 }},
		{"distance", func(s *Spec, i *Inventory) { s.Distance = "Dot" }},
		{"http", func(s *Spec, i *Inventory) { s.Endpoint = "http://example.invalid/v1" }},
		{"userinfo", func(s *Spec, i *Inventory) { s.Endpoint = "https://user:secret@example.invalid/v1" }},
		{"query", func(s *Spec, i *Inventory) { s.Endpoint = "https://example.invalid/v1?key=fake" }},
		{"empty query", func(s *Spec, i *Inventory) { s.Endpoint = "https://example.invalid/v1?" }},
		{"fragment", func(s *Spec, i *Inventory) { s.Endpoint = "https://example.invalid/v1#fake" }},
		{"same target", func(s *Spec, i *Inventory) { s.Target.Messages = s.Target.Schema }},
		{"reused old", func(s *Spec, i *Inventory) { s.Target.Schema = s.Old.Messages }},
		{"invalid name", func(s *Spec, i *Inventory) { s.Target.Schema = " schema " }},
		{"missing old", func(s *Spec, i *Inventory) { i.Collections = i.Collections[:1] }},
		{"one nonzero", func(s *Spec, i *Inventory) { v := uint64(1); i.Collections[1].PointsCount = &v }},
		{"unknown count", func(s *Spec, i *Inventory) { i.Collections[1].PointsCount = nil }},
		{"named vector", func(s *Spec, i *Inventory) { i.Collections[0].VectorType = "named-dense" }},
		{"wrong old size", func(s *Spec, i *Inventory) { i.Collections[1].Dimensions = 1024 }},
		{"wrong old distance", func(s *Spec, i *Inventory) { i.Collections[1].Distance = "Dot" }},
		{"unhealthy", func(s *Spec, i *Inventory) { i.Collections[0].Status = "yellow" }},
		{"target exists", func(s *Spec, i *Inventory) {
			c := i.Collections[0]
			c.Name = s.Target.Schema
			i.Collections = append(i.Collections, c)
		}},
		{"target alias", func(s *Spec, i *Inventory) { i.Aliases = []Alias{{Name: s.Target.Schema, Collection: s.Old.Schema}} }},
		{"old alias", func(s *Spec, i *Inventory) { i.Aliases = []Alias{{Name: "old_alias", Collection: s.Old.Schema}} }},
		{"unknown collections", func(s *Spec, i *Inventory) { i.CollectionsComplete = false }},
		{"unknown aliases", func(s *Spec, i *Inventory) { i.AliasesComplete = false }},
		{"no provenance", func(s *Spec, i *Inventory) { i.Provenance = "" }},
		{"no identity", func(s *Spec, i *Inventory) { i.InstanceID = "" }},
		{"no observation", func(s *Spec, i *Inventory) { i.ObservedAt = time.Time{} }},
		{"duplicate", func(s *Spec, i *Inventory) { i.Collections = append(i.Collections, i.Collections[0]) }},
		{"no freshness limit", func(s *Spec, i *Inventory) { s.MaxInventoryAgeSeconds = 0 }},
		{"unbounded freshness", func(s *Spec, i *Inventory) { s.MaxInventoryAgeSeconds = 86400 }},
		{"source count without evidence", func(s *Spec, i *Inventory) { v := uint64(21); s.Source.EligibleTexts = &v }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, i := syntheticInput()
			tt.change(&s, &i)
			if _, err := PlanBootstrap(s, i); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestPlanEstimateCountsBatchesButNeverInventsDuration(t *testing.T) {
	for _, tt := range []struct{ n, want uint64 }{{0, 0}, {1, 1}, {20, 1}, {21, 2}, {^uint64(0), 922337203685477581}} {
		s, i := syntheticInput()
		s.Source = Source{EligibleTexts: &tt.n, Provenance: "operator inventory of eligible texts"}
		p, err := PlanBootstrap(s, i)
		if err != nil {
			t.Fatal(err)
		}
		if p.Estimate.Requests == nil || *p.Estimate.Requests != tt.want || p.Estimate.DurationKnown || p.HistoricalCoverage != "unknown" {
			t.Fatalf("bad estimate for %d: %+v", tt.n, p)
		}
	}
}

func TestPreflightRejectsStaleDriftAndTamperedPlan(t *testing.T) {
	s, i := syntheticInput()
	p, err := PlanBootstrap(s, i)
	if err != nil {
		t.Fatal(err)
	}
	now := i.ObservedAt.Add(time.Minute)
	current := cloneInventoryForTest(i)
	current.ObservedAt = now
	current.Provenance = "second synthetic audit"
	report, err := CheckPreflight(p, current, now)
	if err != nil || !report.MetadataConsistent || report.LiveReady || report.PauseAttested || report.OperationalRollbackReady {
		t.Fatalf("preflight: %+v %v", report, err)
	}
	cases := []struct {
		name   string
		change func(*Plan, *Inventory, *time.Time)
	}{
		{"stale baseline", func(p *Plan, i *Inventory, n *time.Time) { *n = n.Add(10 * time.Minute); i.ObservedAt = *n }},
		{"future audit", func(p *Plan, i *Inventory, n *time.Time) { i.ObservedAt = n.Add(time.Second) }},
		{"audit older than baseline", func(p *Plan, i *Inventory, n *time.Time) { i.ObservedAt = p.Baseline.ObservedAt.Add(-time.Second) }},
		{"changed instance", func(p *Plan, i *Inventory, n *time.Time) { i.InstanceID = "other" }},
		{"new writes", func(p *Plan, i *Inventory, n *time.Time) { v := uint64(2); i.Collections[1].PointsCount = &v }},
		{"new target", func(p *Plan, i *Inventory, n *time.Time) {
			c := i.Collections[0]
			c.Name = p.Spec.Target.Messages
			i.Collections = append(i.Collections, c)
		}},
		{"unrelated drift", func(p *Plan, i *Inventory, n *time.Time) {
			c := i.Collections[0]
			c.Name = "other"
			i.Collections = append(i.Collections, c)
		}},
		{"changed endpoint", func(p *Plan, i *Inventory, n *time.Time) { p.Spec.Endpoint = "https://other.invalid" }},
		{"forged ready", func(p *Plan, i *Inventory, n *time.Time) { p.LiveReady = true }},
		{"forged pause", func(p *Plan, i *Inventory, n *time.Time) { p.PauseAttested = true }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pp := p
			ii := cloneInventoryForTest(current)
			nn := now
			tt.change(&pp, &ii, &nn)
			r, e := CheckPreflight(pp, ii, nn)
			if e == nil || r.MetadataConsistent || r.LiveReady {
				t.Fatalf("drift accepted: %+v, %v", r, e)
			}
		})
	}
}

func cloneInventoryForTest(i Inventory) Inventory {
	b, _ := json.Marshal(i)
	var c Inventory
	_ = json.Unmarshal(b, &c)
	return c
}

func TestSimulationPreservesOldAndValidatesBeforeSelection(t *testing.T) {
	s, i := syntheticInput()
	p, err := PlanBootstrap(s, i)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, _ := json.Marshal(p.Baseline.Collections)
	result, err := SimulateBootstrap(p, Scenario{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected != s.Target || result.Phase != "selected-paused" || result.LiveReady || result.PauseAttested || !result.SimulationOnly {
		t.Fatalf("bad result %+v", result)
	}
	found := map[string]Collection{}
	for _, c := range result.Collections {
		found[c.Name] = c
	}
	for _, c := range i.Collections {
		if !reflect.DeepEqual(found[c.Name], c) {
			t.Fatalf("old changed: %+v", found[c.Name])
		}
	}
	for _, name := range []string{s.Target.Schema, s.Target.Messages} {
		c := found[name]
		if c.Dimensions != 1024 || c.Distance != "Cosine" || c.VectorType != "dense" || c.PointsCount == nil || *c.PointsCount != 0 {
			t.Fatalf("bad new geometry: %+v", c)
		}
	}
	validated, selected := -1, -1
	for n, e := range result.Events {
		if e.Action == "validate-targets" {
			validated = n
		}
		if e.Action == "select-targets" {
			selected = n
		}
	}
	if validated < 0 || selected <= validated {
		t.Fatalf("selection before validation: %+v", result.Events)
	}
	after, _ := json.Marshal(p.Baseline.Collections)
	if string(after) != string(oldBytes) {
		t.Fatal("simulation mutated plan")
	}
}

func TestSimulationFailuresLeaveOldSelectedAndNoDeletion(t *testing.T) {
	for _, stage := range []string{"create-schema", "create-messages", "validate-targets", "select-targets"} {
		t.Run(stage, func(t *testing.T) {
			s, i := syntheticInput()
			p, _ := PlanBootstrap(s, i)
			r, e := SimulateBootstrap(p, Scenario{FailAt: stage})
			if e != nil {
				t.Fatal(e)
			}
			if r.Failure == "" || r.Selected != s.Old || r.Phase != "failed" {
				t.Fatalf("unsafe failure: %+v", r)
			}
			for _, old := range i.Collections {
				found := false
				for _, c := range r.Collections {
					if c.Name == old.Name {
						found = true
						if !reflect.DeepEqual(old, c) {
							t.Fatal("old mutated")
						}
					}
				}
				if !found {
					t.Fatal("old deleted")
				}
			}
			if stage == "create-messages" && len(r.Collections) != 3 {
				t.Fatalf("partial create not represented: %+v", r)
			}
		})
	}
}

func TestSimulationRollbackStopsAfterWritesResume(t *testing.T) {
	s, i := syntheticInput()
	p, _ := PlanBootstrap(s, i)
	r, err := SimulateBootstrap(p, Scenario{Rollback: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Selected != s.Old || r.Phase != "rolled-back-paused" || r.NewMaintenanceRequired {
		t.Fatalf("pre-resume rollback: %+v", r)
	}
	r, err = SimulateBootstrap(p, Scenario{Rollback: true, ResumeWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Selected != s.Target || r.Phase != "rollback-blocked" || !r.NewMaintenanceRequired || r.InstantRollbackAllowed {
		t.Fatalf("stale route rollback allowed: %+v", r)
	}
	for _, c := range r.Collections {
		if c.Name == s.Old.Schema && *c.PointsCount != 0 {
			t.Fatal("old modified")
		}
		if c.Name == s.Target.Schema && *c.PointsCount != 1 {
			t.Fatal("resumed write not modeled")
		}
	}
	if _, err = SimulateBootstrap(p, Scenario{FailAt: "drop-old"}); err == nil {
		t.Fatal("unsupported action accepted")
	}
}

func TestEndpointContractAndErrorsNeverEchoCanary(t *testing.T) {
	const canary = "SYNTHETIC_SECRET_CANARY_7x"
	for _, endpoint := range []string{
		"https://embedding.example.invalid/v1",
		"https://embedding.example.invalid/compatible-mode/v1/embeddings",
		"https://embedding.example.invalid/compatible-mode/v1#",
		"https://" + canary + ":password@example.invalid/compatible-mode/v1",
		"https://example.invalid/compatible-mode/v1?key=" + canary,
		"https://example.invalid/compatible-mode/v1?" + canary,
		"https://%" + canary + "/compatible-mode/v1",
	} {
		s, i := syntheticInput()
		s.Endpoint = endpoint
		p, err := PlanBootstrap(s, i)
		if err == nil {
			t.Fatalf("invalid endpoint accepted: %q", endpoint)
		}
		b, _ := json.Marshal(p)
		if strings.Contains(err.Error(), canary) || strings.Contains(string(b), canary) {
			t.Fatal("secret canary echoed in rejection")
		}
	}
	s, i := syntheticInput()
	s.Endpoint += "/"
	if _, err := PlanBootstrap(s, i); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightFreshnessBoundaryAndCollectionOrdering(t *testing.T) {
	s, i := syntheticInput()
	p, _ := PlanBootstrap(s, i)
	current := cloneInventoryForTest(i)
	current.Collections[0], current.Collections[1] = current.Collections[1], current.Collections[0]
	if r, err := CheckPreflight(p, current, i.ObservedAt.Add(300*time.Second)); err != nil || !r.MetadataConsistent {
		t.Fatalf("boundary failed: %+v %v", r, err)
	}
	if _, err := CheckPreflight(p, current, i.ObservedAt.Add(301*time.Second)); err == nil {
		t.Fatal("stale inventory accepted")
	}
}

func TestStateMachineRejectsSelectionAndWritesBeforeValidation(t *testing.T) {
	s, _ := syntheticInput()
	store := memoryStore{collections: map[string]Collection{}, selected: s.Old, target: s.Target}
	if err := store.selectTargets(); err == nil || store.selected != s.Old {
		t.Fatal("selected targets without validation")
	}
	if err := store.resumeWrites(); err == nil {
		t.Fatal("writes resumed before validation")
	}
	if err := store.create(s.Target.Schema, s); err != nil {
		t.Fatal(err)
	}
	if err := store.create(s.Target.Schema, s); err == nil {
		t.Fatal("existing target overwritten")
	}
	if err := store.validate(s); err == nil {
		t.Fatal("partial pair validated")
	}
	if err := store.selectTargets(); err == nil {
		t.Fatal("selected partial pair")
	}
}

func TestPlanRequiresExplicitEmptyAliasListing(t *testing.T) {
	s, i := syntheticInput()
	i.Aliases = nil
	if _, err := PlanBootstrap(s, i); err == nil {
		t.Fatal("missing alias listing treated as verified empty")
	}
}

func TestPlanRejectsApproximateCountsAndUnknownProvenance(t *testing.T) {
	for _, idx := range []int{0, 1} {
		s, i := syntheticInput()
		i.Collections[idx].PointsCountExact = false
		if _, err := PlanBootstrap(s, i); err == nil {
			t.Fatal("approximate zero count accepted")
		}
	}
	for _, provenance := range []string{"unknown", " UNKNOWN ", "\t"} {
		s, i := syntheticInput()
		i.Provenance = provenance
		if _, err := PlanBootstrap(s, i); err == nil {
			t.Fatal("unknown inventory provenance accepted")
		}
	}
	s, i := syntheticInput()
	p, err := PlanBootstrap(s, i)
	if err != nil {
		t.Fatal(err)
	}
	i.Collections[0].PointsCountExact = false
	if r, err := CheckPreflight(p, i, i.ObservedAt); err == nil || r.MetadataConsistent {
		t.Fatal("recheck accepted loss of exact-count evidence")
	}
}

func TestPlanRejectsUnknownIdentityAndSourceEstimateProvenance(t *testing.T) {
	for _, placeholder := range []string{"unknown", "unverified", " UNKNOWN ", " UNVERIFIED "} {
		s, i := syntheticInput()
		i.InstanceID = placeholder
		if _, err := PlanBootstrap(s, i); err == nil {
			t.Fatal("unknown instance identity accepted")
		}
		s, i = syntheticInput()
		i.Provenance = placeholder
		if _, err := PlanBootstrap(s, i); err == nil {
			t.Fatal("unknown provenance accepted")
		}
		s, i = syntheticInput()
		n := uint64(21)
		s.Source = Source{EligibleTexts: &n, Provenance: placeholder}
		if _, err := PlanBootstrap(s, i); err == nil {
			t.Fatal("unknown source estimate provenance accepted")
		}
	}
}

func TestSimulationExplicitlyDisclaimsOperationalRollback(t *testing.T) {
	s, i := syntheticInput()
	p, err := PlanBootstrap(s, i)
	if err != nil {
		t.Fatal(err)
	}
	r, err := SimulateBootstrap(p, Scenario{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err = json.Unmarshal(b, &output); err != nil {
		t.Fatal(err)
	}
	value, present := output["operational_rollback_ready"]
	if !present || value != false {
		t.Fatal("simulation must explicitly disclaim operational rollback readiness")
	}
}
