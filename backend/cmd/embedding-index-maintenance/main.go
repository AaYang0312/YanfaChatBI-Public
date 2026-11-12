// embedding-index-maintenance is an offline JSON planner, not a live executor.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"yanfachatbi/pkg/embeddingmaintenance"
)

const help = `Usage: embedding-index-maintenance plan|preflight|simulate
       embedding-index-maintenance --help

Read one JSON object from stdin (maximum 1 MiB); emit JSON on stdout.
Errors are JSON on stderr with a nonzero exit status.

plan:      {"spec":{...},"inventory":{...}} -> Plan
preflight: {"plan":{...},"inventory":{...},"now":"RFC3339"} -> Preflight
simulate:  {"plan":{...},"scenario":{"fail_at":"","rollback":false,"resume_writes":false}} -> Simulation

Simulation fail_at: create-schema, create-messages, validate-targets, select-targets.
Simulation failures are reported as scenario outcomes, not live execution failures.

No network, credential reads, source exports, live apply/cutover/rebuild, or deletion.
JSON consistency is not live readiness, pause attestation, or operational rollback proof.
Zero existing vectors do not establish historical source coverage or an empty business corpus.
Inventory requires explicit complete collection/alias arrays and points_count_exact=true.
Unknown/unverified identity or inventory/source-estimate provenance is rejected.
Baseline and recheck must both be fresh (explicit 1-900 second maximum age).
A plan digest binds metadata but is not a signature or evidence authenticity check.
Simulation speed is never a production ETA.

Synthetic fixture: pkg/embeddingmaintenance/testdata/synthetic-plan-input.json
`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errOut io.Writer) int {
	fail := func(err error, code int) int {
		_ = json.NewEncoder(errOut).Encode(map[string]string{"error": err.Error()})
		return code
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := io.WriteString(out, help); err != nil {
			return fail(errors.New("could not write help"), 1)
		}
		return 0
	}
	if len(args) != 1 {
		return fail(errors.New("exactly one offline command required: plan, preflight, or simulate; use --help"), 2)
	}
	switch args[0] {
	case "plan", "preflight", "simulate":
	default:
		return fail(errors.New("unsupported command; only offline plan, preflight, and simulate are available"), 2)
	}
	const maximum = 1 << 20
	data, err := io.ReadAll(io.LimitReader(in, maximum+1))
	if err != nil {
		return fail(errors.New("could not read input"), 2)
	}
	if len(data) > maximum {
		return fail(errors.New("input exceeds 1 MiB limit"), 2)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fail(errors.New("input must be one JSON object"), 2)
	}
	decode := func(target any) error {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			return errors.New("invalid JSON input or unsupported field; provide only the documented nonsecret metadata")
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return errors.New("input must contain exactly one JSON object")
		}
		return nil
	}
	var result any
	switch args[0] {
	case "plan":
		var request struct {
			Spec      embeddingmaintenance.Spec      `json:"spec"`
			Inventory embeddingmaintenance.Inventory `json:"inventory"`
		}
		if err = decode(&request); err != nil {
			return fail(err, 2)
		}
		result, err = embeddingmaintenance.PlanBootstrap(request.Spec, request.Inventory)
	case "preflight":
		var request struct {
			Plan      embeddingmaintenance.Plan      `json:"plan"`
			Inventory embeddingmaintenance.Inventory `json:"inventory"`
			Now       time.Time                      `json:"now"`
		}
		if err = decode(&request); err != nil {
			return fail(err, 2)
		}
		result, err = embeddingmaintenance.CheckPreflight(request.Plan, request.Inventory, request.Now)
	case "simulate":
		var request struct {
			Plan     embeddingmaintenance.Plan     `json:"plan"`
			Scenario embeddingmaintenance.Scenario `json:"scenario"`
		}
		if err = decode(&request); err != nil {
			return fail(err, 2)
		}
		result, err = embeddingmaintenance.SimulateBootstrap(request.Plan, request.Scenario)
	}
	if err != nil {
		return fail(err, 1)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fail(errors.New("could not encode result"), 1)
	}
	if _, err = fmt.Fprintln(out, string(encoded)); err != nil {
		return fail(errors.New("could not write result"), 1)
	}
	return 0
}
