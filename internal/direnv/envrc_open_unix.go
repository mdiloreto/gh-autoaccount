//go:build linux || darwin

package direnv

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readEnvrcNoFollow(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, fmt.Errorf("refusing changed or non-regular .envrc: %s", path)
	}
	return io.ReadAll(f)
}
