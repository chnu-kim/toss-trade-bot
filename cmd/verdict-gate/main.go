// Command verdict-gate is the thin I/O layer the privileged, base-defined
// .github/workflows/verdict-gate.yml job calls into. All judgement logic
// lives in internal/gate (ADR-0008/ADR-0011) — this binary only does JSON
// in/out plus an exit code the workflow can branch on. It never reads a PR
// diff from a checked-out PR branch and never executes anything the diff
// contains; every subcommand takes already-fetched data on stdin (the
// workflow fetches that data via the GitHub API, as inert text) and prints a
// decision.
//
// Subcommands:
//
//	classify         risk-classify a PR's changed paths (ADR-0008 point 5)
//	eligibility      the ADR-0011 point 4(f) AND guard
//	parse-diff       turn raw `gh pr diff` text into sanity's diff_files input
//	sanity           the ADR-0011 point 4(e)(iii) independent sanity check
//	retry-decision   the ADR-0011 point 4(e)(iv) SHA-sticky/N/M retry policy
//	combine          the ADR-0011 point 4(g) per-verdict-check leg combiner
//
// Every subcommand: reads one JSON object from stdin, writes one JSON object
// to stdout, and sets an exit code a workflow `if` step can test directly —
// 0 for the "proceed" outcome (non-critical, eligible, sane, produce,
// approve), 1 for the fail-closed outcome. Malformed input is always exit 2
// (distinct from a fail-closed *judgement* so a workflow step can tell "the
// gate said no" apart from "something is wrong with this command").
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/chnu-kim/toss-trade-bot/internal/gate"
)

const exitMalformedInput = 2

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: verdict-gate <classify|eligibility|sanity|retry-decision|combine>")
		os.Exit(exitMalformedInput)
	}

	var code int
	var err error
	switch os.Args[1] {
	case "classify":
		code, err = runClassify(os.Args[2:], os.Stdin, os.Stdout)
	case "eligibility":
		code, err = runEligibility(os.Stdin, os.Stdout)
	case "parse-diff":
		code, err = runParseDiff(os.Stdin, os.Stdout)
	case "sanity":
		code, err = runSanity(os.Stdin, os.Stdout)
	case "retry-decision":
		code, err = runRetryDecision(os.Stdin, os.Stdout)
	case "combine":
		code, err = runCombine(os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(exitMalformedInput)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitMalformedInput)
	}
	os.Exit(code)
}

// --- classify ---

type classifyInput struct {
	Paths []string `json:"paths"`
}

type mappingFile struct {
	Rules []gate.PathRule `json:"rules"`
}

type classifyOutput struct {
	Class gate.RiskClass `json:"class"`
}

func runClassify(args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	if len(args) < 1 {
		return 0, fmt.Errorf("classify: usage: verdict-gate classify <mapping-file-path>")
	}
	mappingRaw, err := os.ReadFile(args[0])
	if err != nil {
		return 0, fmt.Errorf("classify: reading mapping file: %w", err)
	}
	var mapping mappingFile
	if err := json.Unmarshal(mappingRaw, &mapping); err != nil {
		return 0, fmt.Errorf("classify: malformed mapping file: %w", err)
	}

	var in classifyInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, fmt.Errorf("classify: malformed stdin: %w", err)
	}

	class := gate.ClassifyChangedPaths(in.Paths, mapping.Rules)
	if err := json.NewEncoder(stdout).Encode(classifyOutput{Class: class}); err != nil {
		return 0, err
	}
	if class == gate.RiskCritical {
		return 1, nil // exit 1 signals "N-of-2 required" to the workflow
	}
	return 0, nil
}

// --- eligibility ---

type eligibilityInput struct {
	HeadRepo string `json:"head_repo"`
	BaseRepo string `json:"base_repo"`
	Author   string `json:"author"`
}

type eligibilityOutput struct {
	Eligible bool `json:"eligible"`
}

func runEligibility(stdin io.Reader, stdout io.Writer) (int, error) {
	var in eligibilityInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, fmt.Errorf("eligibility: malformed stdin: %w", err)
	}
	eligible := gate.Eligible(gate.PRContext{HeadRepo: in.HeadRepo, BaseRepo: in.BaseRepo, Author: in.Author})
	if err := json.NewEncoder(stdout).Encode(eligibilityOutput{Eligible: eligible}); err != nil {
		return 0, err
	}
	if !eligible {
		return 1, nil
	}
	return 0, nil
}

// --- parse-diff ---

type parseDiffOutput struct {
	DiffFiles []gate.DiffFile `json:"diff_files"`
}

// runParseDiff reads raw unified-diff text (e.g. the output of
// `gh pr diff --patch`, fetched by the workflow as inert data — ADR-0011
// point 4(b)) from stdin and emits the diff_files JSON the sanity
// subcommand's stdin schema expects. It never touches disk or shells out;
// see internal/gate.ParseUnifiedDiff.
func runParseDiff(stdin io.Reader, stdout io.Writer) (int, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0, fmt.Errorf("parse-diff: reading stdin: %w", err)
	}
	files := gate.ParseUnifiedDiff(string(raw))
	if err := json.NewEncoder(stdout).Encode(parseDiffOutput{DiffFiles: files}); err != nil {
		return 0, err
	}
	return 0, nil
}

// --- sanity ---

type sanityInput struct {
	// Verdict is kept as raw JSON (not gate.Verdict) so runSanity can route
	// it through gate.ParseVerdict's schema validation itself — a leg's
	// malformed/schema-invalid raw output must fail closed to
	// Indeterminate exactly like a failed evidence cross-reference
	// (ADR-0011 point 4(e)(iii)), not silently unmarshal into a
	// partially-zero gate.Verdict that then reads as trivially "sane".
	Verdict   json.RawMessage `json:"verdict"`
	DiffFiles []gate.DiffFile `json:"diff_files"`
	PRText    []string        `json:"pr_text"`
}

type sanityOutput struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func runSanity(stdin io.Reader, stdout io.Writer) (int, error) {
	var in sanityInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, fmt.Errorf("sanity: malformed stdin: %w", err)
	}

	v, err := gate.ParseVerdict(in.Verdict)
	if err != nil {
		// Schema-invalid leg output is fail-closed Indeterminate (exit 1),
		// not a CLI usage error (exit 2) — ADR-0011 point 4(e)(iii).
		if encErr := json.NewEncoder(stdout).Encode(sanityOutput{OK: false, Reason: err.Error()}); encErr != nil {
			return 0, encErr
		}
		return 1, nil
	}

	ok, failure := gate.SanityCheck(v, in.DiffFiles, in.PRText)
	if err := json.NewEncoder(stdout).Encode(sanityOutput{OK: ok, Reason: failure.Reason}); err != nil {
		return 0, err
	}
	if !ok {
		return 1, nil
	}
	return 0, nil
}

// --- retry-decision ---

type retryDecisionInput struct {
	SHAOutcomeRecorded    bool   `json:"sha_outcome_recorded"`
	ExistingSHAOutcome    string `json:"existing_sha_outcome"`
	PRNonApproveStreak    int    `json:"pr_non_approve_streak"`
	GlobalNonApproveCount int    `json:"global_non_approve_count"`
	// PRIntervention/GlobalIntervention must be booleans the *caller*
	// (the workflow) has already derived from a verified GitHub API read
	// (a chnu-kim review submission / a chnu-kim-actor workflow_dispatch —
	// ADR-0011 point 4(e)(iv)). This command does not, and cannot, accept a
	// label or issue-comment source for either — see
	// internal/gate.TestInterventionSignal_OnlyOwnerSourcesConstruct.
	PRIntervention     bool `json:"pr_intervention"`
	GlobalIntervention bool `json:"global_intervention"`
}

type retryDecisionOutput struct {
	Produce bool   `json:"produce"`
	Reason  string `json:"reason"`
}

func parseOutcome(s string) (gate.Outcome, error) {
	switch s {
	case "", "approve":
		return gate.OutcomeApprove, nil
	case "reject":
		return gate.OutcomeReject, nil
	case "indeterminate":
		return gate.OutcomeIndeterminate, nil
	default:
		return 0, fmt.Errorf("unrecognized outcome %q", s)
	}
}

func runRetryDecision(stdin io.Reader, stdout io.Writer) (int, error) {
	var in retryDecisionInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, fmt.Errorf("retry-decision: malformed stdin: %w", err)
	}
	existing, err := parseOutcome(in.ExistingSHAOutcome)
	if err != nil {
		return 0, fmt.Errorf("retry-decision: %w", err)
	}

	h := gate.VerdictHistory{
		SHAOutcomeRecorded:    in.SHAOutcomeRecorded,
		ExistingSHAOutcome:    existing,
		PRNonApproveStreak:    in.PRNonApproveStreak,
		GlobalNonApproveCount: in.GlobalNonApproveCount,
	}
	if in.PRIntervention {
		h.PRIntervention = gate.PRReviewSubmittedByOwner()
	}
	if in.GlobalIntervention {
		h.GlobalIntervention = gate.GlobalWorkflowDispatchByOwner()
	}

	decision := gate.ShouldProduceVerdict(h, gate.DefaultLimits())
	if err := json.NewEncoder(stdout).Encode(retryDecisionOutput{Produce: decision.Produce, Reason: decision.Reason}); err != nil {
		return 0, err
	}
	if !decision.Produce {
		return 1, nil
	}
	return 0, nil
}

// --- combine ---

type combineInput struct {
	Outcomes []string `json:"outcomes"`
}

type combineOutput struct {
	Outcome string `json:"outcome"`
}

func runCombine(stdin io.Reader, stdout io.Writer) (int, error) {
	var in combineInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, fmt.Errorf("combine: malformed stdin: %w", err)
	}
	outcomes := make([]gate.Outcome, 0, len(in.Outcomes))
	for _, s := range in.Outcomes {
		o, err := parseOutcome(s)
		if err != nil {
			return 0, fmt.Errorf("combine: %w", err)
		}
		outcomes = append(outcomes, o)
	}
	combined := gate.CombineLegOutcomes(outcomes)
	if err := json.NewEncoder(stdout).Encode(combineOutput{Outcome: combined.String()}); err != nil {
		return 0, err
	}
	if combined != gate.OutcomeApprove {
		return 1, nil
	}
	return 0, nil
}
