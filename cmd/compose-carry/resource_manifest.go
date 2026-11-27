package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

const manifestUsage = `compose-carry resource-manifest --config compose.json --out resources.json
Exports only referenced external volume/network names and service references, without Docker access.
Names can still be sensitive. Exporting a manifest does not verify any resource.
Exit: 0 exported (including an empty list); 2 input/output error.
`

func runResourceManifest(args []string, out, errOut io.Writer) int {
	fail := func(message string) int { fmt.Fprintln(errOut, "error:", message); return 2 }
	flags := flag.NewFlagSet("resource-manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var config, output string
	flags.StringVar(&config, "config", "", "rendered Compose JSON")
	flags.StringVar(&output, "out", "", "new resource manifest")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			if _, err := io.WriteString(out, manifestUsage); err != nil {
				return fail("cannot write help")
			}
			return 0
		}
		return fail("invalid resource-manifest arguments; use resource-manifest --help")
	}
	if flags.NArg() != 0 || config == "" || output == "" {
		return fail("resource-manifest requires --config and --out")
	}
	data, err := carry.ReadInput(config)
	if err != nil {
		return fail(err.Error())
	}
	resources, err := carry.ParseResources(data)
	if err != nil {
		return fail(err.Error())
	}
	manifest := carry.ResourceManifest{Kind: carry.ResourceManifestKind, Version: 1, Resources: resources}
	if err := carry.WriteResourceManifest(output, manifest); err != nil {
		return fail(err.Error())
	}
	if _, err := fmt.Fprintf(out, "Exported %d external resources. No Docker queries performed; export is not verification. Treat names as sensitive metadata.\n", len(resources)); err != nil {
		return fail("cannot write export summary")
	}
	return 0
}
