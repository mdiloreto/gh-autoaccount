package profileexec

import (
	"fmt"
	"os"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

// SessionUserEnv overrides account selection for a shell session. It is
// distinct from the GH_AUTOPROFILE_USER marker that every proxied child
// inherits, which must never act as an override.
const SessionUserEnv = "GH_AUTOACCOUNT_USER"

// SessionUser returns the session override, or "" when it is unset.
func SessionUser() (string, error) {
	user := os.Getenv(SessionUserEnv)
	if user == "" {
		return "", nil
	}
	if !config.ValidLogin(user) {
		return "", fmt.Errorf("%s is not a valid GitHub login", SessionUserEnv)
	}
	return user, nil
}
