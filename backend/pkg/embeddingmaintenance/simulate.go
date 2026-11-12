package embeddingmaintenance

import (
	"errors"
	"sort"
)

// memoryStore has no delete operation. Transitions operate on real isolated
// collection metadata, and selection is impossible until validation succeeds.
type memoryStore struct {
	collections   map[string]Collection
	selected      Pair
	target        Pair
	validated     bool
	writesResumed bool
	events        []Event
}

func (m *memoryStore) record(action string) {
	m.events = append(m.events, Event{Action: action, Selected: m.selected})
}
func (m *memoryStore) create(name string, s Spec) error {
	if _, ok := m.collections[name]; ok {
		return errors.New("target already exists")
	}
	zero := uint64(0)
	m.collections[name] = Collection{Name: name, VectorType: "dense", Dimensions: s.Dimensions, Distance: s.Distance, PointsCount: &zero, PointsCountExact: true, Status: "green"}
	return nil
}
func (m *memoryStore) validate(s Spec) error {
	for _, name := range []string{s.Target.Schema, s.Target.Messages} {
		c, ok := m.collections[name]
		if !ok || c.VectorType != "dense" || c.Dimensions != s.Dimensions || c.Distance != s.Distance || c.PointsCount == nil || !c.PointsCountExact || *c.PointsCount != 0 || c.Status != "green" {
			return errors.New("fresh pair validation failed")
		}
	}
	m.validated = true
	m.record("validate-targets")
	return nil
}
func (m *memoryStore) selectTargets() error {
	if !m.validated || m.writesResumed {
		return errors.New("target selection requires validated targets and a simulated paused state")
	}
	m.selected = m.target
	m.record("select-targets")
	return nil
}
func (m *memoryStore) resumeWrites() error {
	if m.selected != m.target || !m.validated {
		return errors.New("cannot resume modeled writes before target selection")
	}
	m.writesResumed = true
	// One synthetic point models divergence. No text, vector, or real payload is read.
	c := m.collections[m.target.Schema]
	one := uint64(1)
	c.PointsCount = &one
	m.collections[c.Name] = c
	m.record("resume-writes")
	return nil
}
func (m *memoryStore) rollback(old Pair) error {
	if m.writesResumed {
		return errors.New("instant rollback prohibited after writes resume: old route may be stale; new maintenance and rebuild/reconciliation required")
	}
	m.selected = old
	m.record("rollback-old-route")
	return nil
}

// SimulateBootstrap exercises an isolated in-memory state machine. The paused
// state and route switch are hypothetical, never attestations of a live pause,
// working old embedding provider, or atomic live cross-service cutover.
func SimulateBootstrap(plan Plan, scenario Scenario) (Simulation, error) {
	if _, err := CheckPreflight(plan, plan.Baseline, plan.Baseline.ObservedAt); err != nil {
		return Simulation{}, err
	}
	switch scenario.FailAt {
	case "", "create-schema", "create-messages", "validate-targets", "select-targets":
	default:
		return Simulation{}, errors.New("unsupported simulation failure stage")
	}
	m := memoryStore{collections: map[string]Collection{}, selected: plan.Spec.Old, target: plan.Spec.Target}
	for _, c := range plan.Baseline.Collections {
		m.collections[c.Name] = cloneCollection(c)
	}
	m.record("begin-simulated-pause")
	result := Simulation{SimulationOnly: true, HistoricalCoverage: "unknown", Phase: "selected-paused"}
	finish := func() Simulation {
		result.Selected = m.selected
		result.Events = m.events
		names := make([]string, 0, len(m.collections))
		for name := range m.collections {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			result.Collections = append(result.Collections, cloneCollection(m.collections[name]))
		}
		result.InstantRollbackAllowed = m.validated && m.selected == m.target && !m.writesResumed && result.Phase != "failed"
		return result
	}
	fail := func(err error) (Simulation, error) {
		result.Phase = "failed"
		result.Failure = err.Error()
		return finish(), nil
	}
	for _, step := range []struct{ action, name string }{{"create-schema", plan.Spec.Target.Schema}, {"create-messages", plan.Spec.Target.Messages}} {
		if scenario.FailAt == step.action {
			return fail(errors.New("injected " + step.action + " failure"))
		}
		if err := m.create(step.name, plan.Spec); err != nil {
			return fail(err)
		}
		m.record(step.action)
	}
	if scenario.FailAt == "validate-targets" {
		c := m.collections[m.target.Messages]
		c.Dimensions = 768
		m.collections[c.Name] = c
	}
	if err := m.validate(plan.Spec); err != nil {
		return fail(err)
	}
	if scenario.FailAt == "select-targets" {
		return fail(errors.New("injected select-targets failure before selection"))
	}
	if err := m.selectTargets(); err != nil {
		return fail(err)
	}
	if scenario.ResumeWrites {
		if err := m.resumeWrites(); err != nil {
			return fail(err)
		}
		result.Phase = "writes-resumed"
	}
	if scenario.Rollback {
		if err := m.rollback(plan.Spec.Old); err != nil {
			result.Phase = "rollback-blocked"
			result.Failure = err.Error()
			result.NewMaintenanceRequired = true
			m.record("reject-stale-rollback")
		} else {
			result.Phase = "rolled-back-paused"
		}
	}
	return finish(), nil
}
