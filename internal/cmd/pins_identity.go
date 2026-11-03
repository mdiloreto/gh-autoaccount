package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
)

// Caller holds the mutation lock until envrc/auth cleanup is finished.
func savePinsAndIdentity(registry *config.PinRegistry) error {
	path, err := config.PinsFilePath()
	if err != nil {
		return err
	}
	before, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err := config.SavePins(registry); err != nil {
		return err
	}
	if err := gitidentity.Sync(registry); err != nil {
		var restoreErr error
		if os.IsNotExist(readErr) {
			restoreErr = os.Remove(path)
		} else {
			restoreErr = config.WriteAtomic(path, before)
		}
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("cannot restore pin registry: %w", restoreErr))
		}
		return err
	}
	return nil
}
