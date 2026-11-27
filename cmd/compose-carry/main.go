package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/565431hzhang/compose-carry/internal/buildinfo"
	"github.com/565431hzhang/compose-carry/internal/carry"
)

const usage = `compose-carry ` + buildinfo.Version + ` — read-only host-path checks before a Compose move

Recommended (Linux; prepare Compose JSON first):
  compose-carry export --config compose.json
  compose-carry check [--docker-context NAME] [--all]
  Transfer the default carry.json between hosts yourself.

Advanced commands:
  compose-carry source --config compose.json --out source.json
  compose-carry target --manifest source.json [--path-map paths.json] --out target.json
  compose-carry compare --source source.json --target target.json --format text|json
    [--only-problems] [--fail-on-excluded]
  compose-carry resource-manifest --config compose.json --out resources.json
  compose-carry resources (--config compose.json | --manifest resources.json) --docker-context NAME
    [--resource-map names.json] [--format text|json]

Resources queries Docker (possibly remote) for external volume/network existence only.

Compare options: --only-problems hides matched entries, not exclusions.
--fail-on-excluded returns 1 for recorded exclusions, not uninventoried resources.

Run source on the source Linux host and target on the target Linux host.
Export config with: docker compose config --format json
Output files must not exist. Treat snapshots as sensitive metadata.
Exit codes: 0 scoped checks match; 1 gaps/unverified/no checks/policy failure; 2 input/runtime error.
`

type compareOutput struct {
	carry.Report
	OnlyProblems   bool `json:"only_problems"`
	FailOnExcluded bool `json:"fail_on_excluded"`
}

func prepareReport(report carry.Report, onlyProblems, failOnExcluded bool) compareOutput {
	if failOnExcluded && report.Excluded > 0 {
		report.ExitCode = 1
	}
	if onlyProblems {
		results := make([]carry.Result, 0, len(report.Results))
		for _, result := range report.Results {
			if result.Status != "matched" {
				results = append(results, result)
			}
		}
		report.Results = results
	}
	return compareOutput{Report: report, OnlyProblems: onlyProblems, FailOnExcluded: failOnExcluded}
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errOut, "error:", err); return 2 }
	if len(args) == 0 {
		fmt.Fprint(errOut, usage)
		return 2
	}
	if args[0] == "--help" || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(out, usage)
		return 0
	}
	if args[0] == "--version" {
		if _, err := fmt.Fprintf(out, "compose-carry %s (commit %s)\n", buildinfo.Version, buildinfo.Commit); err != nil {
			return fail(fmt.Errorf("cannot write version"))
		}
		return 0
	}
	if args[0] == "export" || args[0] == "check" {
		return runBundle(args[0], args[1:], out, errOut, queryDocker)
	}
	if args[0] == "resource-manifest" {
		return runResourceManifest(args[1:], out, errOut)
	}
	if args[0] == "resources" {
		return runResources(args[1:], out, errOut, queryDocker)
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config, manifest, output, sourcePath, targetPath, format, pathMap string
	var onlyProblems, failOnExcluded bool
	switch args[0] {
	case "source":
		flags.StringVar(&config, "config", "", "Compose JSON")
		flags.StringVar(&output, "out", "", "new snapshot")
	case "target":
		flags.StringVar(&manifest, "manifest", "", "source snapshot")
		flags.StringVar(&pathMap, "path-map", "", "JSON object mapping exact source paths to target paths")
		flags.StringVar(&output, "out", "", "new snapshot")
	case "compare":
		flags.StringVar(&sourcePath, "source", "", "source snapshot")
		flags.StringVar(&targetPath, "target", "", "target snapshot")
		flags.StringVar(&format, "format", "text", "text or json")
		flags.BoolVar(&onlyProblems, "only-problems", false, "hide matched entries; retain full summary")
		flags.BoolVar(&failOnExcluded, "fail-on-excluded", false, "return 1 when recorded exclusions exist")
	default:
		return fail(fmt.Errorf("unknown command; use --help"))
	}
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(out, usage)
			return 0
		}
		return fail(fmt.Errorf("invalid arguments; use --help"))
	}
	if flags.NArg() != 0 {
		return fail(fmt.Errorf("unexpected positional arguments"))
	}
	readSnapshot := func(path string) (carry.Snapshot, error) {
		data, err := carry.ReadInput(path)
		if err != nil {
			return carry.Snapshot{}, err
		}
		return carry.DecodeSnapshot(data)
	}
	if args[0] == "compare" {
		if sourcePath == "" || targetPath == "" || (format != "text" && format != "json") {
			return fail(fmt.Errorf("compare requires --source, --target, and --format text|json"))
		}
		source, err := readSnapshot(sourcePath)
		if err != nil {
			return fail(err)
		}
		target, err := readSnapshot(targetPath)
		if err != nil {
			return fail(err)
		}
		fullReport, err := carry.Compare(source, target)
		if err != nil {
			return fail(err)
		}
		report := prepareReport(fullReport, onlyProblems, failOnExcluded)
		if format == "json" {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				return fail(fmt.Errorf("cannot write report"))
			}
		} else {
			if _, err := fmt.Fprintln(out, report.Scope); err != nil {
				return fail(fmt.Errorf("cannot write report"))
			}
			s := report.Summary
			if _, err := fmt.Fprintf(out, "Full summary: matched=%d target_missing=%d type_mismatch=%d source_unverified=%d target_unverified=%d excluded=%d\n", s.Matched, s.TargetMissing, s.TypeMismatch, s.SourceUnverified, s.TargetUnverified, s.Excluded); err != nil {
				return fail(fmt.Errorf("cannot write report"))
			}
			if _, err := fmt.Fprintf(out, "View: only_problems=%t; fail_on_excluded=%t; shown=%d of %d entries\n", report.OnlyProblems, report.FailOnExcluded, len(report.Results), report.Checked+report.Excluded); err != nil {
				return fail(fmt.Errorf("cannot write report"))
			}
			if report.OnlyProblems && len(report.Results) == 0 && report.Checked > 0 {
				if _, err := fmt.Fprintln(out, "No in-scope problems; matched entries hidden. Application readiness is not verified."); err != nil {
					return fail(fmt.Errorf("cannot write report"))
				}
			}
			if report.Checked == 0 {
				if _, err := fmt.Fprintln(out, "No in-scope paths to check; not a successful verification."); err != nil {
					return fail(fmt.Errorf("cannot write report"))
				}
			}
			for _, r := range report.Results {
				if _, err := fmt.Fprintf(out, "%-18s service=%q kind=%s source_path=%q target_path=%q ref=%q expected=%s source=%s/%s target=%s/%s\n", r.Status, r.Dependency.Service, r.Dependency.Kind, r.Dependency.Path, r.TargetPath, r.Dependency.Reference, r.Expected, r.Source.State, r.Source.Type, r.Target.State, r.Target.Type); err != nil {
					return fail(fmt.Errorf("cannot write report"))
				}
			}
			if _, err := fmt.Fprintf(out, "In-scope paths: %d; excluded entries: %d; exit: %d\n", report.Checked, report.Excluded, report.ExitCode); err != nil {
				return fail(fmt.Errorf("cannot write report"))
			}
		}
		return report.ExitCode
	}
	if output == "" || (args[0] == "source" && config == "") || (args[0] == "target" && manifest == "") {
		return fail(fmt.Errorf("missing required input or --out; use --help"))
	}
	var snapshot carry.Snapshot
	var err error
	if args[0] == "source" {
		var data []byte
		data, err = carry.ReadInput(config)
		if err == nil {
			var deps []carry.Dependency
			deps, err = carry.ParseCompose(data)
			if err == nil {
				snapshot, err = carry.Source(deps)
			}
		}
	} else {
		var source carry.Snapshot
		source, err = readSnapshot(manifest)
		var paths map[string]string
		if err == nil && pathMap != "" {
			var data []byte
			data, err = carry.ReadInput(pathMap)
			if err == nil {
				paths, err = carry.ParsePathMap(data)
			}
		}
		if err == nil {
			snapshot, err = carry.TargetWithPaths(source, paths)
		}
	}
	if err != nil {
		return fail(err)
	}
	if err := carry.WriteSnapshot(output, snapshot); err != nil {
		return fail(err)
	}
	code := carry.SnapshotExitCode(snapshot)
	if _, err := fmt.Fprintf(out, "Wrote %s snapshot (%d entries). Exit: %d.\n%s\n", snapshot.Phase, len(snapshot.Entries), code, carry.Scope); err != nil {
		return fail(fmt.Errorf("cannot write summary"))
	}
	return code
}
