package main

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestBundleArguments(t *testing.T) {
	for _, args := range [][]string{{"export", "--help"}, {"check", "--help"}} {
		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) != 0 || !strings.Contains(out.String(), "carry.json") {
			t.Fatal(out.String(), stderr.String())
		}
		if run(args, brokenWriter{}, &stderr) != 2 {
			t.Fatal("ignored help output failure")
		}
	}
	for _, args := range [][]string{{"export", "--unknown=private-value"}, {"check", "extra"}, {"check", "--format", "xml"}, {"export"}} {
		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) != 2 || strings.Contains(stderr.String(), "private-value") {
			t.Fatal(args, stderr.String())
		}
	}
	if runtime.GOOS != "linux" {
		var out, stderr bytes.Buffer
		never := func(context.Context, ...string) ([]byte, error) {
			t.Fatal("queried Docker outside Linux")
			return nil, nil
		}
		if runBundle("check", nil, &out, &stderr, never) != 2 || !strings.Contains(stderr.String(), "requires Linux") {
			t.Fatal(stderr.String())
		}
	}
}
