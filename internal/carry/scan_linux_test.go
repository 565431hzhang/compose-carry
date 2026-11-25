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

func TestLinuxMappedTarget(t *testing.T) {
	root := t.TempDir()
	old, moved := filepath.Join(root, "old"), filepath.Join(root, "moved")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(old, "child")
	if err := os.WriteFile(child, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deps := []Dependency{
		dependency("app", "bind", "ref", old),
		dependency("worker", "bind", "shared", old),
		dependency("app", "config", "child", child),
		dependency("app", "excluded", "volume", ""),
		dependency("app", "bind", "relative", "./data"),
	}
	source, err := Source(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	target, err := TargetWithPaths(source, map[string]string{old: moved})
	if err != nil || target.Version != MappedVersion {
		t.Fatal(target, err)
	}
	for i, e := range target.Entries {
		if source.Entries[i].TargetPath != nil || e.Dependency != source.Entries[i].Dependency {
			t.Fatal("changed source identity")
		}
	}
	report, err := Compare(source, target)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range report.Results {
		counts[r.Status]++
		if r.Dependency.Path == child && r.TargetPath != child {
			t.Fatal("mapping applied as prefix")
		}
		if r.Dependency.Path == old && r.TargetPath != moved {
			t.Fatal("shared path not mapped")
		}
	}
	if counts["matched"] != 2 || counts["target_missing"] != 1 || counts["excluded"] != 1 || counts["source_unverified"] != 1 {
		t.Fatal(counts)
	}
	for _, paths := range []map[string]string{{root: moved}, {old: "relative"}, {"./data": moved}, {"": moved}} {
		if _, err := TargetWithPaths(source, paths); err == nil {
			t.Fatal("accepted invalid mapping", paths)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(moved, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, status string }{
		{filepath.Join(moved, "child"), "type_mismatch"},
		{filepath.Join(root, "absent"), "target_missing"},
		{filepath.Join(link, "child"), "target_unverified"},
	} {
		s := Snapshot{Version: Version, Phase: "source", Entries: source.Entries[:1]}
		d, err := TargetWithPaths(s, map[string]string{old: tc.path})
		if err != nil {
			t.Fatal(err)
		}
		r, err := Compare(s, d)
		if err != nil || r.Results[0].Status != tc.status {
			t.Fatal(r, err)
		}
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
