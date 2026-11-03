//go:build linux

package direnv

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// verifyLocalMetadata fails closed if ownership or POSIX ACLs cannot be checked.
func verifyLocalMetadata(path string, info os.FileInfo, allowRoot bool) error {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return fmt.Errorf("cannot inspect filesystem for %s: %w", path, err)
	}
	switch uint64(fs.Type) {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.TMPFS_MAGIC:
	default:
		return fmt.Errorf("unsupported filesystem for ACL inspection: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && !(allowRoot && stat.Uid == 0)) {
		return fmt.Errorf("untrusted owner: %s", path)
	}
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if name == "system.posix_acl_default" && !info.IsDir() {
			continue
		}
		_, err := unix.Lgetxattr(path, name, nil)
		if err != unix.ENODATA {
			return fmt.Errorf("ACL present or uninspectable on %s (%s): %v", path, name, err)
		}
	}
	return nil
}
