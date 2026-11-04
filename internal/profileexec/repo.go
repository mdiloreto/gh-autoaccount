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
	// Conflicts also reports owner pins matched by more than one remote.
	Conflicts bool
}

// RepoRef identifies an accessed repository by host and owner.
type RepoRef struct {
	Host  string
	Owner string
}

type selection struct {
	pin       *config.Pin
	source    string
	conflicts []string
}

// SelectPin applies the pin precedence below an explicit user: an owner pin
// matching the accessed repository, then the deepest directory pin. The second
// result is the selection source.
func SelectPin(registry *config.PinRegistry, dir string, query *RepoQuery) (*config.Pin, string, error) {
	sel, err := selectPin(registry, dir, query)
	return sel.pin, sel.source, err
}

func selectPin(registry *config.PinRegistry, dir string, query *RepoQuery) (selection, error) {
	var sel selection
	// Detection can cost a git subprocess, so skip it without owner pins.
	if query != nil && registry.HasOwnerPins() {
		remotes := &remoteCache{dir: dir}
		if ref, ok := detectRepo(query, remotes); ok {
			if pin := registry.FindOwnerPin(ref.Host, ref.Owner); pin != nil {
				sel = selection{pin: pin, source: "owner"}
			}
		}
		if query.Conflicts {
			sel.conflicts = remotes.conflicts(registry)
		}
	}
	if sel.pin == nil {
		pin, err := registry.FindPinStrict(dir)
		if err != nil {
			return sel, err
		}
		if pin != nil {
			sel.pin, sel.source = pin, "pin"
		}
	}
	return sel, nil
}

// detectRepo prefers sources that name the repository explicitly and reads
// the working directory's remotes, at most one git subprocess, last.
func detectRepo(query *RepoQuery, remotes *remoteCache) (RepoRef, bool) {
	defaultHost := defaultRepoHost()
	if query.GH {
		if isRepoClone(query.Args) {
			// gh ignores -R and GH_REPO when cloning.
			if spec, found := cloneTarget(query.Args); found {
				if ref, ok := parseRepoSpec(spec, defaultHost); ok {
					return ref, true
				}
			}
			// A bare REPO has no owner.
			return remotes.selected()
		}
		if spec, found := repoFlag(query.Args); found {
			return parseRepoSpec(spec, defaultHost)
		}
		if spec := os.Getenv("GH_REPO"); spec != "" {
			return parseRepoSpec(spec, defaultHost)
		}
	}
	return remotes.selected()
}

func defaultRepoHost() string {
	if host := os.Getenv("GH_HOST"); host != "" {
		return host
	}
	return "github.com"
}

// repoFlag finds gh's -R/--repo value. gh's flag table is not known here, and a
// value-taking flag consumes the next token whatever it looks like, so any
// candidate (-R x, -Rx, -R=x, --repo x, --repo=x) right after another bare flag
// might be that flag's value (`-t --repo=x`) and never selects. A candidate at
// the start or right after a non-flag token is unambiguous in every form.
func repoFlag(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--" {
			return "", false
		}
		ambiguous := i > 0 && isBareFlag(args[i-1])
		switch {
		case ambiguous:
		case strings.HasPrefix(arg, "--repo="):
			return strings.TrimPrefix(arg, "--repo="), true
		case strings.HasPrefix(arg, "-R="):
			return strings.TrimPrefix(arg, "-R="), true
		case arg == "-R" || arg == "--repo":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		case strings.HasPrefix(arg, "-R") && !strings.HasPrefix(arg, "--"):
			return strings.TrimPrefix(arg, "-R"), true
		}
	}
	return "", false
}

func isBareFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && arg != "--" && !strings.Contains(arg, "=")
}

func isRepoClone(args []string) bool {
	return len(args) >= 2 && args[0] == "repo" && args[1] == "clone"
}

// cloneTarget returns the first positional argument of `gh repo clone`.
func cloneTarget(args []string) (string, bool) {
	if !isRepoClone(args) {
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

// newRepoRef rejects empty components: an empty host must not default to
// github.com and match a github.com owner pin.
func newRepoRef(host, owner, repo string) (RepoRef, bool) {
	if host == "" || owner == "" || repo == "" {
		return RepoRef{}, false
	}
	host, err := config.NormalizeHost(host)
	if err != nil {
		return RepoRef{}, false
	}
	return RepoRef{Host: host, Owner: owner}, true
}

// remoteCache reads the working directory's remotes with one git subprocess.
type remoteCache struct {
	dir            string
	loaded         bool
	urls, resolved map[string]string
}

func (c *remoteCache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.urls, c.resolved = map[string]string{}, map[string]string{}
	out, err := osexec.Command("git", "-C", c.dir, "config", "--null", "--get-regexp", `^remote\..*\.(url|gh-resolved)$`).Output()
	if err != nil {
		return
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		key, _ = strings.CutPrefix(key, "remote.")
		if name, ok := strings.CutSuffix(key, ".gh-resolved"); ok {
			c.resolved[name] = value
		} else if name, ok := strings.CutSuffix(key, ".url"); ok {
			if _, seen := c.urls[name]; !seen {
				c.urls[name] = value
			}
		}
	}
}

// selected picks the repository gh would treat as the working directory's: the
// remote marked gh-resolved, else upstream, github, origin in gh's order, else
// the only remote.
func (c *remoteCache) selected() (RepoRef, bool) {
	c.load()
	if len(c.resolved) > 0 {
		names := make([]string, 0, len(c.resolved))
		for name := range c.resolved {
			names = append(names, name)
		}
		sort.Strings(names)
		name := names[0]
		remote, remoteOK := parseRemoteURL(c.urls[name])
		if c.resolved[name] == "base" {
			return remote, remoteOK
		}
		defaultHost := defaultRepoHost()
		if remoteOK {
			defaultHost = remote.Host
		}
		return parseRepoSpec(c.resolved[name], defaultHost)
	}
	for _, name := range []string{"upstream", "github", "origin"} {
		if raw, ok := c.urls[name]; ok {
			return parseRemoteURL(raw)
		}
	}
	if len(c.urls) == 1 {
		for _, raw := range c.urls {
			return parseRemoteURL(raw)
		}
	}
	return RepoRef{}, false
}

// conflicts lists the owner pins matched by any remote when there is more than
// one: Git identity matches every remote while credentials follow one.
func (c *remoteCache) conflicts(registry *config.PinRegistry) []string {
	c.load()
	matched := map[string]bool{}
	for _, raw := range c.urls {
		if ref, ok := parseRemoteURL(raw); ok && registry.FindOwnerPin(ref.Host, ref.Owner) != nil {
			matched[ref.Host+"/"+strings.ToLower(ref.Owner)] = true
		}
	}
	if len(matched) < 2 {
		return nil
	}
	owners := make([]string, 0, len(matched))
	for owner := range matched {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	return owners
}
