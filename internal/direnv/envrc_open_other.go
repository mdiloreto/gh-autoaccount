//go:build !linux && !darwin

package direnv

import (
	"fmt"
	"os"
)

func readEnvrcNoFollow(path string) ([]byte, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, err
	}
	return nil, fmt.Errorf("cannot safely inspect existing .envrc on this platform: %s", path)
}
