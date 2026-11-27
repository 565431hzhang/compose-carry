package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

const bundleUsage = `Recommended Linux workflow (prepare rendered Compose JSON first):
  compose-carry export --config compose.json [--out carry.json]
  compose-carry check [--bundle carry.json] [--docker-context NAME]
    [--path-map paths.json] [--resource-map names.json] [--all]
    [--fail-on-excluded] [--format text|json]
Transfer only carry.json yourself. Export never queries Docker; success means written, not verified.
Check scans local paths; a Docker context may point elsewhere. No context means external resources stay unverified.
Default output hides successful rows, not exclusions. --all changes display only.
Strict exclusions apply to the path section, even when external resources are present.
Export exits 0 when written, 2 on errors. Check exits 0 for scoped success, 1 for gaps/unverified/no checks, 2 on errors.
`

func runBundle(command string, args []string, out, errOut io.Writer, runner resourceRunner) int {
	fail := func(err error) int { fmt.Fprintln(errOut, "error:", err); return 2 }
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config, output, bundle, contextName, pathMap, resourceMap, format string
	var all, strict bool
	if command == "export" {
		flags.StringVar(&config, "config", "", "rendered Compose JSON")
		flags.StringVar(&output, "out", "carry.json", "new check bundle")
	} else {
		flags.StringVar(&bundle, "bundle", "carry.json", "source check bundle")
		flags.StringVar(&contextName, "docker-context", "", "explicit Docker context for external resources")
		flags.StringVar(&pathMap, "path-map", "", "exact path mappings")
		flags.StringVar(&resourceMap, "resource-map", "", "exact resource name mappings")
		flags.StringVar(&format, "format", "text", "text or json")
		flags.BoolVar(&all, "all", false, "show successful rows as well")
		flags.BoolVar(&strict, "fail-on-excluded", false, "fail on recorded path exclusions")
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			if _, err := io.WriteString(out, bundleUsage); err != nil {
				return fail(fmt.Errorf("cannot write help"))
			}
			return 0
		}
		return fail(fmt.Errorf("invalid %s arguments; use %s --help", command, command))
	}
	if flags.NArg() != 0 {
		return fail(fmt.Errorf("unexpected positional arguments"))
	}
	if runtime.GOOS != "linux" {
		return fail(fmt.Errorf("export/check host scanning requires Linux; use compare for offline checks on other platforms"))
	}
	if command == "export" {
		if config == "" || output == "" {
			return fail(fmt.Errorf("export requires --config and a nonempty output path"))
		}
		data, err := carry.ReadInput(config)
		if err != nil {
			return fail(err)
		}
		b, err := carry.ExportBundle(data)
		if err != nil {
			return fail(err)
		}
		if err := carry.WriteBundle(output, b); err != nil {
			return fail(err)
		}
		unverified, excluded, checked := 0, 0, 0
		for _, e := range b.Paths.Entries {
			if e.Dependency.Kind == "excluded" {
				excluded++
				continue
			}
			checked++
			if e.Source.State != "observed" || e.Source.Type != e.Expected {
				unverified++
			}
		}
		_, err = fmt.Fprintf(out, "Exported %q: paths=%d source_unverified=%d path_excluded=%d external_resources=%d (not queried).\nExport is not verification. Names and paths may be sensitive.\nTransfer this file, then run check --bundle with its destination path; add --docker-context NAME for external resources.\n", output, checked, unverified, excluded, len(b.Resources.Resources))
		if err != nil {
			return fail(fmt.Errorf("cannot write export summary; bundle was already written"))
		}
		if checked+len(b.Resources.Resources) == 0 {
			if _, err := fmt.Fprintln(out, "No in-scope dependencies to check."); err != nil {
				return fail(fmt.Errorf("cannot write export summary"))
			}
		}
		return 0
	}
	contextSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "docker-context" {
			contextSet = true
		}
	})
	if bundle == "" || (format != "text" && format != "json") || (contextSet && !validDockerContext(contextName)) {
		return fail(fmt.Errorf("invalid check bundle, format or Docker context"))
	}
	data, err := carry.ReadInput(bundle)
	if err != nil {
		return fail(err)
	}
	b, err := carry.DecodeBundle(data)
	if err != nil {
		return fail(err)
	}
	var paths map[string]string
	var mapping carry.ResourceMap
	if pathMap != "" {
		data, err = carry.ReadInput(pathMap)
		if err != nil {
			return fail(err)
		}
		paths, err = carry.ParsePathMap(data)
		if err != nil {
			return fail(err)
		}
	}
	if resourceMap != "" {
		data, err = carry.ReadInput(resourceMap)
		if err != nil {
			return fail(err)
		}
		mapping, err = carry.ParseResourceMap(data)
		if err != nil {
			return fail(err)
		}
	}
	report, err := carry.CheckBundle(b, paths, mapping, contextName, resourceQuery(contextName, runner), all, strict)
	if err != nil {
		return fail(err)
	}
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		err = enc.Encode(report)
	} else {
		err = writeBundleReport(out, report)
	}
	if err != nil {
		return fail(fmt.Errorf("cannot write check report"))
	}
	return report.ExitCode
}

func writeBundleReport(out io.Writer, r carry.BundleReport) error {
	state := "SCOPED CHECKS PASSED"
	if r.ExitCode != 0 {
		state = "GAPS / UNVERIFIED / NO CHECKS"
	}
	p, s := r.Paths.Summary, r.Resources.Summary
	if _, err := fmt.Fprintf(out, "%s (exit %d)\n%s\nPaths: matched=%d missing=%d type_mismatch=%d source_unverified=%d target_unverified=%d path_excluded=%d\nResources: present=%d missing=%d unverified=%d context=%q\nView: only_problems=%t; fail_on_excluded=%t\n", state, r.ExitCode, r.Scope, p.Matched, p.TargetMissing, p.TypeMismatch, p.SourceUnverified, p.TargetUnverified, p.Excluded, s.Present, s.Missing, s.Unverified, r.Resources.DockerContext, r.OnlyProblems, r.FailOnExcluded); err != nil {
		return err
	}
	if r.Paths.Excluded > 0 {
		if _, err := fmt.Fprintln(out, "Path exclusions may overlap resource checks; strict mode does not remove them."); err != nil {
			return err
		}
	}
	for _, item := range r.Paths.Results {
		if _, err := fmt.Fprintf(out, "path %-18s service=%q kind=%s ref=%q source_path=%q target_path=%q expected=%s source=%s/%s target=%s/%s\n", item.Status, item.Dependency.Service, item.Dependency.Kind, item.Dependency.Reference, item.Dependency.Path, item.TargetPath, item.Expected, item.Source.State, item.Source.Type, item.Target.State, item.Target.Type); err != nil {
			return err
		}
	}
	for _, item := range r.Resources.Results {
		refs, _ := json.Marshal(item.References)
		if _, err := fmt.Fprintf(out, "resource %-10s kind=%s name=%q checked_name=%q references=%s reason=%s hint=%q\n", item.Status, item.Kind, item.Name, item.CheckedName, refs, item.Reason, item.Hint); err != nil {
			return err
		}
	}
	if r.ExitCode == 0 && r.OnlyProblems {
		_, err := fmt.Fprintln(out, "No in-scope problems; successful rows hidden. Application readiness is not verified.")
		return err
	}
	return nil
}
