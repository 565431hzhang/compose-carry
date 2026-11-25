package carry

import (
	"errors"
	"sort"
)

const Scope = "Only listed bind mounts and file configs/secrets: existence and type, not content, permissions, named volumes, networks, or application readiness."

type Result struct {
	Dependency Dependency  `json:"dependency"`
	Expected   string      `json:"expected"`
	Source     Observation `json:"source"`
	Target     Observation `json:"target"`
	Status     string      `json:"status"`
	TargetPath string      `json:"target_path"`
}

type Report struct {
	Scope    string   `json:"scope"`
	Checked  int      `json:"checked"`
	Excluded int      `json:"excluded"`
	ExitCode int      `json:"exit_code"`
	Results  []Result `json:"results"`
}

func Compare(source, target Snapshot) (Report, error) {
	report := Report{Scope: Scope, Results: []Result{}}
	if err := source.Validate(); err != nil {
		return report, err
	}
	if err := target.Validate(); err != nil {
		return report, err
	}
	if source.Phase != "source" || target.Phase != "target" || len(source.Entries) != len(target.Entries) {
		return report, errors.New("snapshots do not match: phase or dependency set differs")
	}
	targets := map[string]Entry{}
	for _, e := range target.Entries {
		targets[e.Dependency.ID] = e
	}
	for _, s := range source.Entries {
		t, ok := targets[s.Dependency.ID]
		if !ok || s.Dependency != t.Dependency || s.Expected != t.Expected || s.Source != t.Source {
			return Report{}, errors.New("snapshots do not match: dependency or source evidence differs")
		}
		result := Result{Dependency: s.Dependency, Expected: s.Expected, Source: s.Source, Target: *t.Target, TargetPath: t.targetDependency().Path}
		switch {
		case s.Dependency.Kind == "excluded":
			result.Status = "excluded"
			report.Excluded++
		case s.Source.State != "observed" || s.Source.Type != s.Expected:
			result.Status = "source_unverified"
		case t.Target.State == "missing":
			result.Status = "target_missing"
		case t.Target.State == "not_directory":
			result.Status = "type_mismatch"
		case t.Target.State != "observed":
			result.Status = "target_unverified"
		case t.Target.Type != s.Expected:
			result.Status = "type_mismatch"
		default:
			result.Status = "matched"
		}
		if result.Status != "excluded" {
			report.Checked++
			if result.Status != "matched" {
				report.ExitCode = 1
			}
		}
		report.Results = append(report.Results, result)
	}
	if report.Checked == 0 {
		report.ExitCode = 1
	}
	sort.Slice(report.Results, func(i, j int) bool {
		a, b := report.Results[i].Dependency, report.Results[j].Dependency
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.ID < b.ID
	})
	return report, nil
}
