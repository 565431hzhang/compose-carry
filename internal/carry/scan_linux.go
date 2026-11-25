package carry

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// O_PATH obtains metadata without opening file contents, including devices and FIFOs.
const oPath = 0x200000

func openInput(path string) (*os.File, error) {
	// A path replaced with a FIFO between stat and open must not block.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func inspectPath(path string) Observation {
	unknown := func(state string) Observation { return Observation{State: state, Type: "unknown"} }
	failure := func(err error) Observation {
		switch {
		case errors.Is(err, syscall.ENOENT):
			return unknown("missing")
		case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
			return unknown("permission_denied")
		case errors.Is(err, syscall.ENOTDIR):
			return unknown("not_directory")
		default:
			return unknown("io_error")
		}
	}
	fd, err := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return failure(err)
	}
	defer func() { syscall.Close(fd) }()
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, component := range components {
		if component == "" {
			continue
		}
		next, err := syscall.Openat(fd, component, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return failure(err)
		}
		syscall.Close(fd)
		fd = next
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			return failure(err)
		}
		mode := stat.Mode & syscall.S_IFMT
		if mode == syscall.S_IFLNK {
			return unknown("symlink")
		}
		if i < len(components)-1 && mode != syscall.S_IFDIR {
			return unknown("not_directory")
		}
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return failure(err)
	}
	switch stat.Mode & syscall.S_IFMT {
	case syscall.S_IFREG:
		return Observation{State: "observed", Type: "file"}
	case syscall.S_IFDIR:
		return Observation{State: "observed", Type: "directory"}
	default:
		return unknown("special")
	}
}
