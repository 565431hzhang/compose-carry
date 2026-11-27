package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

const resourcesUsage = `compose-carry resources (--config compose.json | --manifest resources.json) --docker-context NAME [--resource-map names.json] [--format text|json]
Checks referenced external volumes/networks through the selected Docker context.
May contact a remote daemon. Does not verify contents, settings or application readiness.
Exit: 0 at least one resource and all present; 1 missing/unverified/no resources; 2 input/output error.
`

type resourceRunner func(context.Context, ...string) ([]byte, error)

type cappedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.exceeded = true
		w.cancel()
		return 0, errors.New("output limit exceeded")
	}
	return w.buffer.Write(p)
}

func dockerEnvironment(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func boundedCommand(ctx context.Context, limit int, program string, args ...string) ([]byte, error) {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &cappedOutput{limit: limit, cancel: cancel}
	cmd := exec.CommandContext(child, program, args...)
	// Even --context default otherwise inherits DOCKER_HOST and TLS overrides.
	cmd.Env = dockerEnvironment(os.Environ())
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if failure := classifyQueryFailure(err, child.Err(), output.exceeded); failure != nil {
		return nil, failure
	}
	return output.buffer.Bytes(), nil
}

func classifyQueryFailure(err, contextErr error, exceeded bool) error {
	var exit *exec.ExitError
	switch {
	case exceeded:
		return carry.QueryOutputLimit
	case errors.Is(contextErr, context.DeadlineExceeded):
		return carry.QueryTimeout
	case errors.Is(contextErr, context.Canceled):
		return carry.QueryCanceled
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return carry.QueryCommandNotFound
	case errors.As(err, &exit):
		return carry.QueryCommandFailed
	case err != nil:
		return carry.QueryIOError
	default:
		return nil
	}
}

func queryDocker(ctx context.Context, args ...string) ([]byte, error) {
	return boundedCommand(ctx, carry.MaxInputBytes, "docker", args...)
}

func runResources(args []string, out, errOut io.Writer, runner resourceRunner) int {
	fail := func(message string) int { fmt.Fprintln(errOut, "error:", message); return 2 }
	flags := flag.NewFlagSet("resources", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config, manifest, resourceMap, dockerContext, format string
	flags.StringVar(&manifest, "manifest", "", "exported resource manifest")
	flags.StringVar(&resourceMap, "resource-map", "", "exact source-to-target resource name map")
	flags.StringVar(&config, "config", "", "rendered Compose JSON")
	flags.StringVar(&dockerContext, "docker-context", "", "explicit Docker context")
	flags.StringVar(&format, "format", "text", "text or json")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			if _, err := io.WriteString(out, resourcesUsage); err != nil {
				return fail("cannot write help")
			}
			return 0
		}
		return fail("invalid resources arguments; use resources --help")
	}
	if flags.NArg() != 0 || (config == "") == (manifest == "") || dockerContext == "" || strings.TrimSpace(dockerContext) != dockerContext || strings.HasPrefix(dockerContext, "-") || strings.IndexFunc(dockerContext, unicode.IsControl) >= 0 || (format != "text" && format != "json") {
		return fail("resources requires exactly one of --config or --manifest, an explicit --docker-context, and --format text|json")
	}
	input := config
	if manifest != "" {
		input = manifest
	}
	data, err := carry.ReadInput(input)
	if err != nil {
		return fail(err.Error())
	}
	var resources []carry.Resource
	if manifest != "" {
		var decoded carry.ResourceManifest
		decoded, err = carry.DecodeResourceManifest(data)
		resources = decoded.Resources
	} else {
		resources, err = carry.ParseResources(data)
	}
	if err != nil {
		return fail(err.Error())
	}
	var mapping carry.ResourceMap
	if resourceMap != "" {
		data, err = carry.ReadInput(resourceMap)
		if err != nil {
			return fail(err.Error())
		}
		mapping, err = carry.ParseResourceMap(data)
		if err != nil {
			return fail(err.Error())
		}
	}
	report, err := carry.CheckResourcesWithMap(resources, dockerContext, mapping, func(kind string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		args := []string{"--context", dockerContext, kind, "ls"}
		if kind == "network" {
			args = append(args, "--no-trunc", "--format", "[{{json .ID}},{{json .Name}}]")
		} else {
			args = append(args, "--format", "{{json .Name}}")
		}
		return runner(ctx, args...)
	})
	if err != nil {
		return fail(err.Error())
	}
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fail("cannot write resource report")
		}
	} else {
		if _, err := fmt.Fprintf(out, "%s\nDocker context: %q\nSummary: present=%d missing=%d unverified=%d\n", report.Scope, report.DockerContext, report.Summary.Present, report.Summary.Missing, report.Summary.Unverified); err != nil {
			return fail("cannot write resource report")
		}
		if len(report.Results) == 0 {
			if _, err := fmt.Fprintln(out, "No referenced external volumes/networks to check; not a successful verification."); err != nil {
				return fail("cannot write resource report")
			}
		}
		for _, result := range report.Results {
			refs, _ := json.Marshal(result.References)
			if _, err := fmt.Fprintf(out, "%-10s kind=%s name=%q checked_name=%q reason=%s references=%s\n", result.Status, result.Kind, result.Name, result.CheckedName, result.Reason, refs); err != nil {
				return fail("cannot write resource report")
			}
			if result.Hint != "" {
				if _, err := fmt.Fprintf(out, "  hint: %s\n", result.Hint); err != nil {
					return fail("cannot write resource report")
				}
			}
		}
		if _, err := fmt.Fprintf(out, "Resources: %d; exit: %d\n", len(report.Results), report.ExitCode); err != nil {
			return fail("cannot write resource report")
		}
	}
	return report.ExitCode
}
