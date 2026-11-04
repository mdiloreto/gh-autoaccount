package profileexec

import (
	"net/url"
	"os"
	osexec "os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

// RepoQuery describes where to look for the repository a command will access.
// A nil query disables owner routing.
type RepoQuery struct {
	// GH marks the command as gh, whose -R/--repo flag, GH_REPO and
	// `repo clone` target take precedence over the working directory.
	GH   bool
	Args []string // gh arguments, without the executable
}

// RepoRef identifies an accessed repository by host and owner.
type RepoRef struct {
	Host  string
	Owner string
}

// SelectPin applies the pin precedence below an explicit user: an owner pin
// matching the accessed repository, then the deepest directory pin. The second
// result is the selection source.
func SelectPin(registry *config.PinRegistry, dir string, query *RepoQuery) (*config.Pin, string, error) {
	// Detection can cost a git subprocess, so skip it without owner pins.
	if query != nil && registry.HasOwnerPins() {
		if ref, ok := detectRepo(query, dir); ok {
			if pin := registry.FindOwnerPin(ref.Host, ref.Owner); pin != nil {
				return pin, "owner", nil
			}
		}
	}
	pin, err := registry.FindPinStrict(dir)
	if err != nil || pin == nil {
		return nil, "", err
	}
	return pin, "pin", nil
}

// detectRepo prefers sources that name the repository explicitly and reads
// the working directory's remotes, at most one git subprocess, last.
func detectRepo(query *RepoQuery, dir string) (RepoRef, bool) {
	defaultHost := defaultRepoHost()
	if query.GH {
		if spec, found := repoFlag(query.Args); found {
			return parseRepoSpec(spec, defaultHost)
		}
		if spec := os.Getenv("GH_REPO"); spec != "" {
			return parseRepoSpec(spec, defaultHost)
		}
		if spec, found := cloneTarget(query.Args); found {
			// A bare REPO resolves under the authenticated user: no owner.
			return parseRepoSpec(spec, defaultHost)
		}
	}
	return remoteRepo(dir)
}

func defaultRepoHost() string {
	if host := os.Getenv("GH_HOST"); host != "" {
		return host
	}
	return "github.com"
}

// repoFlag finds gh's -R/--repo value in the forms pflag accepts.
func repoFlag(args []string) (string, bool) {
	for i, arg := range args {
		switch {
		case arg == "--":
			return "", false
		case arg == "-R" || arg == "--repo":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		case strings.HasPrefix(arg, "--repo="):
			return strings.TrimPrefix(arg, "--repo="), true
		case strings.HasPrefix(arg, "-R") && !strings.HasPrefix(arg, "--"):
			return strings.TrimPrefix(strings.TrimPrefix(arg, "-R"), "="), true
		}
	}
	return "", false
}

// cloneTarget returns the first positional argument of `gh repo clone`.
func cloneTarget(args []string) (string, bool) {
	if len(args) < 2 || args[0] != "repo" || args[1] != "clone" {
		return "", false
	}
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		switch arg := rest[i]; {
		case arg == "--":
			return "", false
		case arg == "-u" || arg == "--upstream-remote-name":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			return arg, true
		}
	}
	return "", false
}

// parseRepoSpec accepts OWNER/REPO, HOST/OWNER/REPO and remote URLs.
func parseRepoSpec(spec, defaultHost string) (RepoRef, bool) {
	if ref, ok := parseRemoteURL(spec); ok {
		return ref, true
	}
	parts := strings.Split(spec, "/")
	host := defaultHost
	switch len(parts) {
	case 2:
	case 3:
		host, parts = parts[0], parts[1:]
	default:
		return RepoRef{}, false
	}
	return newRepoRef(host, parts[0], parts[1])
}

var scpRemote = regexp.MustCompile(`^(?:[^@/:]+@)?([^@/:]+):(.+)$`)

// parseRemoteURL accepts https, ssh and scp-style Git remote URLs.
func parseRemoteURL(raw string) (RepoRef, bool) {
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return RepoRef{}, false
		}
		switch u.Scheme {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return RepoRef{}, false
		}
		host, path = u.Hostname(), u.Path
	} else if m := scpRemote.FindStringSubmatch(raw); m != nil {
		host, path = m[1], m[2]
	} else {
		return RepoRef{}, false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) < 2 {
		return RepoRef{}, false
	}
	return newRepoRef(host, segments[0], segments[1])
}

func newRepoRef(host, owner, repo string) (RepoRef, bool) {
	host, err := config.NormalizeHost(host)
	if err != nil || owner == "" || repo == "" {
		return RepoRef{}, false
	}
	return RepoRef{Host: host, Owner: owner}, true
}

// remoteRepo selects the repository gh would treat as the working directory's:
// the remote marked with gh-resolved, else origin, else the only remote.
func remoteRepo(dir string) (RepoRef, bool) {
	out, err := osexec.Command("git", "-C", dir, "config", "--null", "--get-regexp", `^remote\..*\.(url|gh-resolved)$`).Output()
	if err != nil {
		return RepoRef{}, false
	}
	urls, resolved := map[string]string{}, map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		key, _ = strings.CutPrefix(key, "remote.")
		if name, ok := strings.CutSuffix(key, ".gh-resolved"); ok {
			resolved[name] = value
		} else if name, ok := strings.CutSuffix(key, ".url"); ok {
			if _, seen := urls[name]; !seen {
				urls[name] = value
			}
		}
	}
	if len(resolved) > 0 {
		names := make([]string, 0, len(resolved))
		for name := range resolved {
			names = append(names, name)
		}
		sort.Strings(names)
		name := names[0]
		remote, remoteOK := parseRemoteURL(urls[name])
		if resolved[name] == "base" {
			return remote, remoteOK
		}
		defaultHost := defaultRepoHost()
		if remoteOK {
			defaultHost = remote.Host
		}
		return parseRepoSpec(resolved[name], defaultHost)
	}
	if raw, ok := urls["origin"]; ok {
		return parseRemoteURL(raw)
	}
	if len(urls) == 1 {
		for _, raw := range urls {
			return parseRemoteURL(raw)
		}
	}
	return RepoRef{}, false
}
