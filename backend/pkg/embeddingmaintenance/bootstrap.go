package embeddingmaintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

var physicalName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// PlanBootstrap accepts only the verified empty 768-dimensional dense pair case.
// Its evidence is supplied metadata, never proof that writes are paused or that
// source business data is empty. It never contacts a provider or a database.
func PlanBootstrap(spec Spec, inventory Inventory) (Plan, error) {
	if err := validateSpec(spec); err != nil {
		return Plan{}, err
	}
	if err := validateInventory(spec, inventory); err != nil {
		return Plan{}, err
	}
	spec = cloneSpec(spec)
	p := Plan{Version: 1, Kind: "empty-index-bootstrap", Spec: spec, Baseline: cloneInventory(inventory), HistoricalCoverage: "unknown", PendingPrerequisites: []string{
		"Independently attest a maintenance window and paused/drained writes; JSON cannot attest this.",
		"Identify the historical source corpus and eligible texts; zero vectors do not prove empty business data or completed backfill.",
		"Verify the intended endpoint/provider/model contract and authorize any live work separately; this tool has no live executor.",
		"Re-audit physical collections and aliases immediately before any separately implemented live change.",
		"Validate the fresh pair before route selection; preserve the old pair and prohibit instant rollback after writes resume.",
	}, Estimate: Estimate{BatchSize: 20, Explanation: "Duration unknown: no measured/operator-provided throughput, token quota, or batch-latency calibration. Simulation speed is not a production ETA."}}
	if unknown(inventory.OldProvider) || unknown(inventory.OldModel) {
		p.PendingPrerequisites = append(p.PendingPrerequisites, "Identify and verify the old provider/model and rollback configuration; unknown old embedding identity blocks operational rollback readiness.")
	} else {
		p.PendingPrerequisites = append(p.PendingPrerequisites, "Independently verify the supplied old provider/model and rollback configuration; metadata does not establish operational rollback readiness.")
	}
	if spec.Source.EligibleTexts != nil {
		// Avoid overflow in (n+19)/20.
		n := *spec.Source.EligibleTexts
		r := n / 20
		if n%20 != 0 {
			r++
		}
		p.Estimate.Requests = &r
	} else {
		p.Estimate.Explanation = "Source corpus and eligible-text count unknown. " + p.Estimate.Explanation
	}
	// The digest detects accidental/tampered plan edits, not forged source evidence.
	b, err := json.Marshal(p)
	if err != nil {
		return Plan{}, fmt.Errorf("encode plan metadata: %w", err)
	}
	sum := sha256.Sum256(b)
	p.ID = hex.EncodeToString(sum[:])
	return p, nil
}

func validateSpec(s Spec) error {
	if s.Provider != "openai" || s.Model != "qwen3.7-text-embedding-flash" || s.Dimensions != 1024 || s.Distance != "Cosine" {
		return errors.New("target must explicitly specify openai, qwen3.7-text-embedding-flash, 1024 dimensions, and Cosine")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || s.Endpoint != strings.TrimSpace(s.Endpoint) || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(s.Endpoint, "#") {
		return errors.New("endpoint must be HTTPS with a host and no userinfo, query, or fragment")
	}
	if (u.Path != "/compatible-mode/v1" && u.Path != "/compatible-mode/v1/") || u.RawPath != "" {
		return errors.New("endpoint must use the explicit /compatible-mode/v1 base path")
	}
	names := []string{s.Old.Schema, s.Old.Messages, s.Target.Schema, s.Target.Messages}
	seen := map[string]bool{}
	for _, name := range names {
		if !physicalName.MatchString(name) || seen[name] {
			return errors.New("old and target names must be four distinct physical names: 1-255 ASCII letters, digits, underscore or hyphen, starting with a letter or digit")
		}
		seen[name] = true
	}
	for _, name := range []string{s.Target.Schema, s.Target.Messages} {
		if name == "neobase_schema" || name == "neobase_messages" {
			return errors.New("fresh targets cannot use either legacy collection name")
		}
	}
	if s.MaxInventoryAgeSeconds < 1 || s.MaxInventoryAgeSeconds > 900 {
		return errors.New("max_inventory_age_seconds must be explicit and between 1 and 900")
	}
	if s.Source.EligibleTexts != nil && unknown(s.Source.Provenance) {
		return errors.New("eligible-text estimate requires source provenance")
	}
	return nil
}

func validateInventory(s Spec, i Inventory) error {
	if unknown(i.InstanceID) || unknown(i.Provenance) || i.ObservedAt.IsZero() {
		return errors.New("inventory requires instance identity, observation time, and provenance")
	}
	if !i.CollectionsComplete || !i.AliasesComplete || i.Collections == nil || i.Aliases == nil {
		return errors.New("inventory must include complete physical collection and alias listings")
	}
	if len(i.Aliases) != 0 {
		return errors.New("empty-index bootstrap requires no aliases; resolve aliases before planning")
	}
	collections := map[string]Collection{}
	for _, c := range i.Collections {
		if !physicalName.MatchString(c.Name) {
			return errors.New("inventory contains an invalid physical collection name")
		}
		if _, exists := collections[c.Name]; exists {
			return errors.New("inventory contains duplicate physical collection names")
		}
		if c.VectorType != "dense" || c.Dimensions <= 0 || c.Distance == "" || c.PointsCount == nil || !c.PointsCountExact || c.Status != "green" {
			return errors.New("inventory requires known healthy dense collection geometry and explicit exact point counts (points_count_exact=true)")
		}
		collections[c.Name] = c
	}
	for _, name := range []string{s.Old.Schema, s.Old.Messages} {
		c, ok := collections[name]
		if !ok || c.Dimensions != 768 || c.Distance != "Cosine" || *c.PointsCount != 0 {
			return errors.New("both old physical collections must exist, be healthy dense 768/Cosine, and have explicitly known zero points")
		}
	}
	for _, name := range []string{s.Target.Schema, s.Target.Messages} {
		if _, exists := collections[name]; exists {
			return errors.New("fresh target physical collections must not exist")
		}
	}
	return nil
}

func unknown(value string) bool {
	return strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "unknown") || strings.EqualFold(strings.TrimSpace(value), "unverified")
}

// CheckPreflight checks freshness and consistency of supplied metadata only.
// Even a successful check leaves every operational readiness flag false.
func CheckPreflight(plan Plan, inventory Inventory, now time.Time) (Preflight, error) {
	report := Preflight{HistoricalCoverage: "unknown"}
	expected, err := PlanBootstrap(plan.Spec, plan.Baseline)
	if err != nil {
		return report, fmt.Errorf("invalid plan: %w", err)
	}
	if !reflect.DeepEqual(plan, expected) {
		return report, errors.New("plan content or binding differs from the derived offline plan; regenerate it")
	}
	report.PendingPrerequisites = append([]string(nil), plan.PendingPrerequisites...)
	if now.IsZero() {
		return report, errors.New("preflight requires an explicit current time")
	}
	limit := time.Duration(plan.Spec.MaxInventoryAgeSeconds) * time.Second
	for _, observed := range []time.Time{plan.Baseline.ObservedAt, inventory.ObservedAt} {
		if observed.IsZero() || observed.After(now) || now.Sub(observed) > limit {
			return report, errors.New("baseline or current inventory is stale, missing, or future-dated; re-audit and regenerate the plan")
		}
	}
	if inventory.ObservedAt.Before(plan.Baseline.ObservedAt) {
		return report, errors.New("current inventory predates the bound baseline")
	}
	if err = validateInventory(plan.Spec, inventory); err != nil {
		return report, err
	}
	baseline, current := cloneInventory(plan.Baseline), cloneInventory(inventory)
	// Observation provenance may identify a new audit. All operational metadata
	// must remain unchanged, including unrelated physical collections.
	baseline.ObservedAt = current.ObservedAt
	baseline.Provenance = current.Provenance
	if !reflect.DeepEqual(baseline, current) {
		return report, errors.New("inventory identity or collection/alias metadata drifted from the bound baseline")
	}
	report.MetadataConsistent = true
	return report, nil
}

func cloneSpec(s Spec) Spec {
	if s.Source.EligibleTexts != nil {
		v := *s.Source.EligibleTexts
		s.Source.EligibleTexts = &v
	}
	return s
}
func cloneCollection(c Collection) Collection {
	if c.PointsCount != nil {
		v := *c.PointsCount
		c.PointsCount = &v
	}
	return c
}
func cloneInventory(i Inventory) Inventory {
	cs := make([]Collection, len(i.Collections))
	for n, c := range i.Collections {
		cs[n] = cloneCollection(c)
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].Name < cs[b].Name })
	as := make([]Alias, len(i.Aliases))
	copy(as, i.Aliases)
	sort.Slice(as, func(a, b int) bool {
		if as[a].Name == as[b].Name {
			return as[a].Collection < as[b].Collection
		}
		return as[a].Name < as[b].Name
	})
	i.Collections = cs
	i.Aliases = as
	return i
}
