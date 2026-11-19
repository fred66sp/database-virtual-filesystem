package cli

import (
	"strconv"
	"strings"
	"time"

	"dbvfs/internal/vfs"
)

// Contract limits from NFR-05 (DESIGN §10).
const (
	defaultTimeout = 5 * time.Second
	minTimeout     = 1 * time.Second
	maxTimeout     = 60 * time.Second
	maxLimit       = 1000
	maxDepth       = 4
)

// invocation is one parsed command line, before any storage access.
type invocation struct {
	spec *commandSpec
	args []string // positionals, in order, for the command's own logic

	path  *vfs.Path
	dest  *vfs.Path
	alias string

	json    bool
	session string
	// timeout is validated here (NFR-05) but consumed from WP-02 on: it must
	// become the per-connection busy_timeout and the context deadline; until
	// then the limit is not enforced.
	timeout   time.Duration
	actor     string
	requestID string

	mode    string
	limit   int
	depth   int
	cursor  string
	where   []string
	data    string
	ifMatch string
	format  string

	dryRun bool
	apply  bool

	help bool
}

// parse implements the grammar of DESIGN §3: flags may appear before or after
// the command and positionals, "--" ends flag parsing, and unknown flags are
// usage errors. The standard flag package is not used because it stops at the
// first positional and cannot bind global flags that precede the command.
func parse(args []string) (*invocation, error) {
	inv := &invocation{timeout: defaultTimeout}
	var pos []string
	seen := make(map[string]bool)
	endOfFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if !endOfFlags && isFlagToken(arg) {
			name, value, hasValue := splitFlag(arg)
			if name == "h" {
				name = "help"
			}
			spec, ok := inv.spec.lookupFlag(name)
			if !ok {
				return nil, usageError("unknown flag %q", arg)
			}
			if seen[name] && spec.kind != flagRepeat {
				return nil, usageError("flag %q given more than once", "--"+name)
			}
			seen[name] = true
			if spec.kind == flagBool {
				if hasValue {
					return nil, usageError("flag %q does not take a value", "--"+name)
				}
				inv.setBool(name)
				continue
			}
			if !hasValue {
				if i+1 >= len(args) {
					return nil, usageError("flag %q requires a value", "--"+name)
				}
				i++
				value = args[i]
			}
			if err := inv.setValue(name, value); err != nil {
				return nil, err
			}
			continue
		}
		if inv.spec == nil {
			spec := findCommand(arg)
			if spec == nil {
				return nil, usageError("unknown command %q", arg)
			}
			inv.spec = spec
			continue
		}
		pos = append(pos, arg)
	}

	if inv.spec == nil {
		if inv.help {
			return inv, nil
		}
		return nil, usageError("missing command")
	}
	if inv.help {
		return inv, nil
	}
	inv.args = pos
	if err := inv.validate(pos, seen); err != nil {
		return nil, err
	}
	return inv, nil
}

func isFlagToken(arg string) bool {
	return len(arg) > 1 && arg[0] == '-' && arg != "--"
}

func splitFlag(arg string) (name, value string, hasValue bool) {
	token := arg[1:]
	if strings.HasPrefix(token, "-") {
		token = token[1:]
	}
	return strings.Cut(token, "=")
}

func (inv *invocation) setBool(name string) {
	switch name {
	case "json":
		inv.json = true
	case "help":
		inv.help = true
	case "dry-run":
		inv.dryRun = true
	case "apply":
		inv.apply = true
	}
}

func (inv *invocation) setValue(name, value string) error {
	switch name {
	case "session":
		inv.session = value
	case "timeout":
		n, err := strconv.Atoi(value)
		if err != nil || n < int(minTimeout/time.Second) || n > int(maxTimeout/time.Second) {
			return usageError("--timeout must be an integer number of seconds between 1 and 60")
		}
		inv.timeout = time.Duration(n) * time.Second
	case "actor":
		inv.actor = value
	case "request-id":
		inv.requestID = value
	case "mode":
		if value != "ro" && value != "rw" {
			return usageError("--mode must be ro or rw")
		}
		inv.mode = value
	case "limit":
		n, err := parseIntFlag("--limit", value, 1)
		if err != nil {
			return err
		}
		if n > maxLimit {
			return codedError(CodeLimit, "--limit exceeds maximum %d", maxLimit)
		}
		inv.limit = n
	case "depth":
		n, err := parseIntFlag("--depth", value, 1)
		if err != nil {
			return err
		}
		if n > maxDepth {
			return codedError(CodeLimit, "--depth exceeds maximum %d", maxDepth)
		}
		inv.depth = n
	case "cursor":
		inv.cursor = value
	case "where":
		expr := strings.TrimSpace(value)
		if expr == "" {
			return usageError("--where requires a non-empty expression")
		}
		inv.where = append(inv.where, expr)
	case "data":
		inv.data = value
	case "if-match":
		inv.ifMatch = value
	case "format":
		if value != "json" && value != "jsonl" {
			return usageError("--format must be json or jsonl")
		}
		inv.format = value
	}
	return nil
}

func parseIntFlag(flag, value string, min int) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < min {
		return 0, usageError("%s must be an integer of at least %d", flag, min)
	}
	return n, nil
}

// validate enforces the per-command preconditions documented in DESIGN §3 and
// decodes the virtual paths.
func (inv *invocation) validate(pos []string, seen map[string]bool) error {
	spec := inv.spec
	if len(pos) < spec.argsMin() || len(pos) > spec.argsMax() {
		return usageError("usage: db-ai %s", spec.usage)
	}
	if inv.dryRun && inv.apply {
		return usageError("--dry-run and --apply are mutually exclusive")
	}
	for _, name := range spec.requiredFlags {
		if !seen[name] {
			return usageError("%s requires %q", spec.name, "--"+name)
		}
	}
	if spec.requireSess && inv.session == "" {
		return usageError("%s requires --session", spec.name)
	}
	if spec.name == "mount" && inv.mode == "" {
		inv.mode = "ro"
	}
	if spec.name == "export" && inv.format == "" {
		inv.format = "json"
	}

	for i, role := range spec.roles {
		if i >= len(pos) {
			if role == argOptionalPath {
				if inv.session != "" {
					return codedError(CodeUnsupported, "resolving the session working directory is not implemented until WP-03")
				}
				root := vfs.Path{Kind: vfs.KindRoot}
				inv.path = &root
			}
			continue
		}
		switch role {
		case argPath, argOptionalPath:
			p, err := parsePath(pos[i], inv.session)
			if err != nil {
				return err
			}
			inv.path = &p
		case argDestPath:
			p, err := parsePath(pos[i], inv.session)
			if err != nil {
				return err
			}
			inv.dest = &p
		case argAlias:
			alias, err := parseAlias(pos[i])
			if err != nil {
				return err
			}
			inv.alias = alias
		}
	}
	return nil
}

// parsePath validates one VFS argument. Without a session a relative path is a
// usage error; with a session it is legal but needs session state (WP-03).
func parsePath(raw, session string) (vfs.Path, error) {
	if raw == "" {
		return vfs.Path{}, usageError("path is empty")
	}
	if !strings.HasPrefix(raw, "/") {
		if session == "" {
			return vfs.Path{}, usageError("relative path %q requires --session", raw)
		}
		return vfs.Path{}, codedError(CodeUnsupported, "relative path resolution is not implemented until WP-03")
	}
	return vfs.Parse(raw)
}

func parseAlias(raw string) (string, error) {
	p, err := vfs.Parse(raw)
	if err != nil {
		return "", err
	}
	if p.Kind != vfs.KindMount {
		return "", usageError("mount point %q must be /ALIAS", raw)
	}
	return p.Alias, nil
}
