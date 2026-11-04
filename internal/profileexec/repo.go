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
	// Conflicts also reports owner pins whose Git identity can disagree with
	// the selected credentials.
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
			sel.conflicts = remotes.conflicts(registry, sel.pin)
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
// the working directory's remotes, at most one git subprocess, last. When an
// explicit source is present but cannot be read unambiguously, it reports no
// repository rather than consulting weaker sources: gh may access a repository
// other than the remotes' or GH_REPO's, and a pin for those must not be chosen.
func detectRepo(query *RepoQuery, remotes *remoteCache) (RepoRef, bool) {
	defaultHost := defaultRepoHost()
	if query.GH {
		if isRepoClone(query.Args) {
			// gh ignores -R and GH_REPO when cloning, and a bare REPO resolves
			// under the authenticated account, so it names no owner.
			spec, found := cloneTarget(query.Args)
			if !found {
				return RepoRef{}, false
			}
			return parseRepoSpec(spec, defaultHost)
		}
		var ref RepoRef
		var ok bool
		switch spec, state := repoFlag(query.Args); {
		case state == repoFlagAmbiguous:
			return RepoRef{}, false
		case state == repoFlagFound:
			ref, ok = parseRepoSpec(spec, defaultHost)
		case os.Getenv("GH_REPO") != "":
			ref, ok = parseRepoSpec(os.Getenv("GH_REPO"), defaultHost)
		default:
			ref, ok = remotes.selected()
		}
		if ok && mentionsOtherRepo(query.Args, ref, defaultHost) {
			return RepoRef{}, false
		}
		return ref, ok
	}
	return remotes.selected()
}

// mentionsOtherRepo reports whether a non-flag argument names a repository
// other than ref: a positional OWNER/REPO, HOST/OWNER/REPO or URL
// (`gh repo view other-org/x`, `gh pr view <url>`) or an API path under
// repos/OWNER/REPO. gh may access that repository instead, and the command
// grammar is not known here, so any such mention makes the target unknown.
// A flag value that merely looks like one (`--title fix/x`) also counts; that
// only skips owner routing.
func mentionsOtherRepo(args []string, ref RepoRef, defaultHost string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-R" || arg == "--repo" {
			i++ // the selected repository itself
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// API paths first: repos/OWNER/REPO would otherwise read as HOST/OWNER/REPO.
		mention, ok := apiRepoPath(arg, defaultHost)
		if !ok {
			mention, ok = parseRepoSpec(arg, defaultHost)
		}
		if ok && (mention.Host != ref.Host || !strings.EqualFold(mention.Owner, ref.Owner)) {
			return true
		}
	}
	return false
}

// apiRepoPath reads OWNER from a `gh api` path such as repos/OWNER/REPO/pulls.
func apiRepoPath(arg, defaultHost string) (RepoRef, bool) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(arg, "/"), "repos/")
	if !ok {
		return RepoRef{}, false
	}
	segments := strings.SplitN(rest, "/", 3)
	if len(segments) < 2 {
		return RepoRef{}, false
	}
	return newRepoRef(defaultHost, segments[0], strings.SplitN(segments[1], "?", 2)[0])
}

func defaultRepoHost() string {
	if host := os.Getenv("GH_HOST"); host != "" {
		return host
	}
	return "github.com"
}

type repoFlagState int

const (
	repoFlagNone repoFlagState = iota
	repoFlagFound
	repoFlagAmbiguous
)

// repoFlag finds gh's -R/--repo value. gh's flag table is not known here, and a
// value-taking flag consumes the next token whatever it looks like, so any
// candidate (-R x, -Rx, -R=x, --repo x, --repo=x) right after another bare flag
// might be that flag's value (`-t --repo=x`), or a real -R after a boolean flag.
// Either way the target is unknown, so the candidate is ambiguous. More than one
// candidate is ambiguous too: gh uses the last value given. Only a single
// candidate at the start or right after a non-flag token is found.
func repoFlag(args []string) (string, repoFlagState) {
	spec, state := "", repoFlagNone
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		afterBareFlag := i > 0 && isBareFlag(args[i-1])
		var value string
		switch {
		case strings.HasPrefix(arg, "--repo="):
			value = strings.TrimPrefix(arg, "--repo=")
		case strings.HasPrefix(arg, "-R="):
			value = strings.TrimPrefix(arg, "-R=")
		case arg == "-R" || arg == "--repo":
			if i+1 < len(args) {
				value = args[i+1]
			}
			i++ // the next token is this flag's value
		case strings.HasPrefix(arg, "-R") && !strings.HasPrefix(arg, "--"):
			value = strings.TrimPrefix(arg, "-R")
		case len(arg) > 2 && arg[0] == '-' && arg[1] != '-' && strings.ContainsRune(arg, 'R'):
			// A shorthand cluster such as -wR: whether R is the repo flag
			// depends on the other letters' arity, which is unknown here.
			return "", repoFlagAmbiguous
		default:
			continue
		}
		if afterBareFlag || state != repoFlagNone {
			return "", repoFlagAmbiguous
		}
		spec, state = value, repoFlagFound
	}
	return spec, state
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

// conflicts lists the owner pins matched by any remote when Git identity and
// credentials can disagree: identity matches every remote while credentials
// follow the selected pin. That is the case with several matched owner pins, or
// with one that is not the selected pin.
func (c *remoteCache) conflicts(registry *config.PinRegistry, selected *config.Pin) []string {
	c.load()
	matched := map[string]*config.Pin{}
	for _, raw := range c.urls {
		if ref, ok := parseRemoteURL(raw); ok {
			if pin := registry.FindOwnerPin(ref.Host, ref.Owner); pin != nil {
				matched[ref.Host+"/"+strings.ToLower(ref.Owner)] = pin
			}
		}
	}
	if len(matched) == 0 {
		return nil
	}
	if len(matched) == 1 {
		for _, pin := range matched {
			if pin == selected {
				return nil
			}
		}
	}
	owners := make([]string, 0, len(matched))
	for owner := range matched {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	return owners
}
