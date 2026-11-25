package carry

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLinuxScan(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config")
	if err := os.WriteFile(file, []byte("not-to-be-read"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want Observation
	}{
		{"/", Observation{"observed", "directory"}},
		{root, Observation{"observed", "directory"}},
		{file, Observation{"observed", "file"}},
		{filepath.Join(root, "absent"), Observation{"missing", "unknown"}},
		{link, Observation{"symlink", "unknown"}},
		{filepath.Join(link, "config"), Observation{"symlink", "unknown"}},
		{dangling, Observation{"symlink", "unknown"}},
		{filepath.Join(file, "child"), Observation{"not_directory", "unknown"}},
		{fifo, Observation{"special", "unknown"}},
	}
	for _, c := range cases {
		if got := inspectPath(c.path); got != c.want {
			t.Errorf("%s: %+v want %+v", c.path, got, c.want)
		}
	}
	if err := os.Chmod(file, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(file, 0600)
	if got := inspectPath(file); got.State != "observed" {
		t.Fatal("metadata inspection must not require reading file content", got)
	}
}

func TestLinuxInputFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "input.json")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInput(fifo); err == nil {
		t.Fatal("accepted FIFO input")
	}
	f, err := openInput(fifo)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode().IsRegular() {
		t.Fatal("FIFO must remain identifiable after nonblocking open", err)
	}
}

func TestLinuxPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root user")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(locked, "data")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0700)
	if got := inspectPath(file); got.State != "permission_denied" {
		t.Fatal(got)
	}
}

func TestLinuxSourceTarget(t *testing.T) {
	root := t.TempDir()
	d := dependency("app", "bind", "ref", root)
	source, err := Source([]Dependency{d})
	if err != nil {
		t.Fatal(err)
	}
	target, err := Target(source)
	if err != nil {
		t.Fatal(err)
	}
	if source.Entries[0].Target != nil {
		t.Fatal("target mutated source")
	}
	report, err := Compare(source, target)
	if err != nil || report.ExitCode != 0 {
		t.Fatal(report, err)
	}
	if _, err := Target(target); err == nil {
		t.Fatal("target accepted as source")
	}
}
