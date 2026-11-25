//go:build !linux

package carry

import "os"

func openInput(path string) (*os.File, error) { return os.Open(path) }

func inspectPath(string) Observation { return Observation{State: "io_error", Type: "unknown"} }
