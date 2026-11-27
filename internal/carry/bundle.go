package carry

import (
	"encoding/json"
	"errors"
	"os"
)

const BundleKind = "compose-carry.bundle"

type Bundle struct {
	Kind      string           `json:"kind"`
	Version   int              `json:"version"`
	Paths     Snapshot         `json:"paths"`
	Resources ResourceManifest `json:"resources"`
}

func (b Bundle) Validate() error {
	if b.Kind != BundleKind || b.Version != 1 || b.Paths.Phase != "source" {
		return errors.New("invalid bundle kind, version or source phase")
	}
	if err := b.Paths.Validate(); err != nil {
		return err
	}
	return b.Resources.Validate()
}

func ExportBundle(data []byte) (Bundle, error) {
	deps, err := ParseCompose(data)
	if err != nil {
		return Bundle{}, err
	}
	resources, err := ParseResources(data)
	if err != nil {
		return Bundle{}, err
	}
	source, err := Source(deps)
	if err != nil {
		return Bundle{}, err
	}
	b := Bundle{Kind: BundleKind, Version: 1, Paths: source, Resources: ResourceManifest{Kind: ResourceManifestKind, Version: 1, Resources: resources}}
	return b, b.Validate()
}

func DecodeBundle(data []byte) (Bundle, error) {
	var b Bundle
	invalid := errors.New("invalid bundle JSON")
	if len(data) > MaxInputBytes || checkJSON(data) != nil {
		return b, invalid
	}
	obj, ok := exactKeys(data, "kind", "version", "paths", "resources")
	if !ok {
		return b, invalid
	}
	if json.Unmarshal(obj["kind"], &b.Kind) != nil || json.Unmarshal(obj["version"], &b.Version) != nil {
		return b, invalid
	}
	var err error
	b.Paths, err = DecodeSnapshot(obj["paths"])
	if err != nil {
		return Bundle{}, err
	}
	b.Resources, err = DecodeResourceManifest(obj["resources"])
	if err != nil {
		return Bundle{}, err
	}
	return b, b.Validate()
}

func WriteBundle(filename string, b Bundle) error {
	if err := b.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return errors.New("cannot encode bundle")
	}
	if len(data)+1 > MaxInputBytes {
		return errors.New("bundle exceeds 16 MiB limit")
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create bundle (existing files are never overwritten)")
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot write bundle; output may be incomplete")
	}
	return nil
}

type BundlePaths struct {
	Summary  Summary  `json:"summary"`
	Checked  int      `json:"checked"`
	Excluded int      `json:"excluded"`
	Results  []Result `json:"results"`
}

type BundleResources struct {
	DockerContext string           `json:"docker_context"`
	Summary       ResourceSummary  `json:"summary"`
	Results       []ResourceResult `json:"results"`
}

type BundleReport struct {
	Kind           string          `json:"kind"`
	Version        int             `json:"version"`
	Scope          string          `json:"scope"`
	ExitCode       int             `json:"exit_code"`
	OnlyProblems   bool            `json:"only_problems"`
	FailOnExcluded bool            `json:"fail_on_excluded"`
	Paths          BundlePaths     `json:"paths"`
	Resources      BundleResources `json:"resources"`
}

func CombineReports(paths Report, resources ResourceReport, all, strict bool) BundleReport {
	r := BundleReport{Kind: "compose-carry.check", Version: 1, Scope: "Listed host paths: existence and type only. Referenced external volumes/networks: existence in the selected Docker context only. Contents, permissions and application readiness are not verified.", OnlyProblems: !all, FailOnExcluded: strict,
		Paths:     BundlePaths{Summary: paths.Summary, Checked: paths.Checked, Excluded: paths.Excluded, Results: []Result{}},
		Resources: BundleResources{DockerContext: resources.DockerContext, Summary: resources.Summary, Results: []ResourceResult{}}}
	resourceCount := resources.Summary.Present + resources.Summary.Missing + resources.Summary.Unverified
	if paths.Checked+resourceCount == 0 || paths.Checked != paths.Summary.Matched || resources.Summary.Missing > 0 || resources.Summary.Unverified > 0 || (strict && paths.Excluded > 0) {
		r.ExitCode = 1
	}
	for _, p := range paths.Results {
		if all || p.Status != "matched" {
			r.Paths.Results = append(r.Paths.Results, p)
		}
	}
	for _, resource := range resources.Results {
		if all || resource.Status != "present" {
			r.Resources.Results = append(r.Resources.Results, resource)
		}
	}
	return r
}

func CheckBundle(b Bundle, paths map[string]string, mapping ResourceMap, dockerContext string, query func(string) ([]byte, error), all, strict bool) (BundleReport, error) {
	if err := b.Validate(); err != nil {
		return BundleReport{}, err
	}
	if err := ValidatePathMap(b.Paths, paths); err != nil {
		return BundleReport{}, err
	}
	if err := mapping.Validate(b.Resources.Resources); err != nil {
		return BundleReport{}, err
	}
	target, err := TargetWithPaths(b.Paths, paths)
	if err != nil {
		return BundleReport{}, err
	}
	compared, err := Compare(b.Paths, target)
	if err != nil {
		return BundleReport{}, err
	}
	var resources ResourceReport
	if dockerContext == "" {
		resources = ResourceReport{Scope: ResourceScope, Results: []ResourceResult{}}
		for _, r := range b.Resources.Resources {
			resources.Results = append(resources.Results, ResourceResult{Resource: r, CheckedName: mapping.checkedName(r), Status: "unverified", Reason: "context_required", Hint: "Run check with --docker-context NAME to verify external resources."})
			resources.Summary.Unverified++
		}
	} else {
		resources, err = CheckResourcesWithMap(b.Resources.Resources, dockerContext, mapping, query)
		if err != nil {
			return BundleReport{}, err
		}
	}
	return CombineReports(compared, resources, all, strict), nil
}
