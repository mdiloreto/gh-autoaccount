//go:build !linux

package direnv

import (
	"fmt"
	"os"
)

func verifyLocalMetadata(path string, info os.FileInfo, allowRoot bool) error {
	return fmt.Errorf("automatic authorization cannot verify ownership and ACLs on this platform: %s", path)
}
