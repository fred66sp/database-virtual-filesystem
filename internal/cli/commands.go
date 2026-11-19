package cli

// This file is the declarative half of the CLI contract (DESIGN §3): one entry
// per command with its positional shape, flags, preconditions and the work
// package that will implement its behaviour.

type flagKind int

const (
	flagBool flagKind = iota
	flagString
	flagRepeat
)

// flagSpec is one flag. arg is the placeholder shown in help ("" for booleans).
type flagSpec struct {
	name string
	arg  string
	kind flagKind
	doc  string
}

var (
	flagJSON      = flagSpec{"json", "", flagBool, "accepted for compatibility; JSON is the only output format"}
	flagSession   = flagSpec{"session", "ID", flagString, "session ID that resolves relative paths"}
	flagTimeout   = flagSpec{"timeout", "N", flagString, "operation timeout in seconds, 1-60 (default 5)"}
	flagActor     = flagSpec{"actor", "NAME", flagString, "informational actor label"}
	flagRequestID = flagSpec{"request-id", "ID", flagString, "client request ID; generated when absent"}
	flagHelp      = flagSpec{"help", "", flagBool, "show help (-h also works)"}

	flagMode    = flagSpec{"mode", "ro|rw", flagString, "access mode; rw requires an explicit write policy (default ro)"}
	flagLimit   = flagSpec{"limit", "N", flagString, "maximum records per page, 1-1000"}
	flagCursor  = flagSpec{"cursor", "TOKEN", flagString, "pagination cursor from meta.next_cursor"}
	flagDepth   = flagSpec{"depth", "N", flagString, "tree depth, 1-4"}
	flagWhere   = flagSpec{"where", "EXPR", flagRepeat, "filter as COLUMN=OPVALUE, repeatable (AND)"}
	flagData    = flagSpec{"data", "JSON|-", flagString, "JSON payload, or - to read stdin once"}
	flagIfMatch = flagSpec{"if-match", "ETAG", flagString, "ETag precondition for the change"}
	flagFormat  = flagSpec{"format", "json|jsonl", flagString, "output format, json or jsonl (default json)"}
	flagDryRun  = flagSpec{"dry-run", "", flagBool, "plan the mutation without executing it"}
	flagApply   = flagSpec{"apply", "", flagBool, "confirm the destructive operation (required)"}
)

var globalFlags = []flagSpec{flagJSON, flagSession, flagTimeout, flagActor, flagRequestID, flagHelp}

// argRole says how a positional argument is validated.
type argRole int

const (
	argPlain argRole = iota
	argPath
	argOptionalPath
	argAlias
	argDestPath
)

// commandSpec is one command of DESIGN §3. Positional bounds derive from
// argRoles so the table cannot drift from the usage line.
type commandSpec struct {
	name          string
	summary       string
	usage         string
	roles         []argRole
	flags         []flagSpec
	requiredFlags []string
	requireSess   bool
	wp            string
}

func (s *commandSpec) argsMin() int {
	n := 0
	for _, r := range s.roles {
		if r != argOptionalPath {
			n++
		}
	}
	return n
}

func (s *commandSpec) argsMax() int { return len(s.roles) }

// commands is ordered as in DESIGN §3 and drives both dispatch and help.
var commands = []commandSpec{
	{
		name:    "mount",
		summary: "register a SQLite file under an alias",
		usage:   "mount FILE /ALIAS [--mode ro|rw]",
		roles:   []argRole{argPlain, argAlias},
		flags:   []flagSpec{flagMode},
		wp:      "WP-02",
	},
	{
		name:    "umount",
		summary: "remove a mount; never deletes the file",
		usage:   "umount /ALIAS",
		roles:   []argRole{argAlias},
		wp:      "WP-02",
	},
	{
		name:    "mounts",
		summary: "list registered mounts",
		usage:   "mounts",
		wp:      "WP-02",
	},
	{
		name:    "ls",
		summary: "list children of a path",
		usage:   "ls [PATH] [--limit N] [--cursor TOKEN]",
		roles:   []argRole{argOptionalPath},
		flags:   []flagSpec{flagLimit, flagCursor},
		wp:      "WP-02",
	},
	{
		name:    "tree",
		summary: "list descendants up to a depth",
		usage:   "tree [PATH] [--depth N] [--limit N]",
		roles:   []argRole{argOptionalPath},
		flags:   []flagSpec{flagDepth, flagLimit},
		wp:      "WP-02",
	},
	{
		name:    "stat",
		summary: "show what a path is",
		usage:   "stat PATH",
		roles:   []argRole{argPath},
		wp:      "WP-02",
	},
	{
		name:    "describe",
		summary: "describe a table, its columns and capabilities",
		usage:   "describe PATH",
		roles:   []argRole{argPath},
		wp:      "WP-02",
	},
	{
		name:    "cat",
		summary: "read a row or a field as JSON",
		usage:   "cat PATH",
		roles:   []argRole{argPath},
		wp:      "WP-02",
	},
	{
		name:        "cd",
		summary:     "change the session working directory",
		usage:       "cd PATH",
		roles:       []argRole{argPath},
		requireSess: true,
		wp:          "WP-03",
	},
	{
		name:        "pwd",
		summary:     "print the session working directory",
		usage:       "pwd",
		requireSess: true,
		wp:          "WP-03",
	},
	{
		name:    "find",
		summary: "filter rows with --where expressions",
		usage:   "find PATH [--where EXPR]... [--limit N] [--cursor TOKEN]",
		roles:   []argRole{argPath},
		flags:   []flagSpec{flagWhere, flagLimit, flagCursor},
		wp:      "WP-03",
	},
	{
		name:    "grep",
		summary: "search a literal substring in text columns",
		usage:   "grep PATH TEXT [--limit N] [--cursor TOKEN]",
		roles:   []argRole{argPath, argPlain},
		flags:   []flagSpec{flagLimit, flagCursor},
		wp:      "WP-03",
	},
	{
		name:          "touch",
		summary:       "insert a row, returning its key",
		usage:         "touch PATH --data JSON|-",
		roles:         []argRole{argPath},
		flags:         []flagSpec{flagData, flagDryRun},
		requiredFlags: []string{"data"},
		wp:            "WP-05",
	},
	{
		name:          "write",
		summary:       "update a field or a row with an ETag precondition",
		usage:         "write ROW_OR_FIELD --data JSON|- --if-match ETAG",
		roles:         []argRole{argPath},
		flags:         []flagSpec{flagData, flagIfMatch, flagDryRun},
		requiredFlags: []string{"data", "if-match"},
		wp:            "WP-05",
	},
	{
		name:          "cp",
		summary:       "copy a row within its table",
		usage:         "cp ROW DEST_ROW --if-match ETAG",
		roles:         []argRole{argPath, argDestPath},
		flags:         []flagSpec{flagIfMatch, flagDryRun},
		requiredFlags: []string{"if-match"},
		wp:            "WP-05",
	},
	{
		name:          "mv",
		summary:       "rename a simple primary key",
		usage:         "mv ROW DEST_ROW --if-match ETAG --apply",
		roles:         []argRole{argPath, argDestPath},
		flags:         []flagSpec{flagIfMatch, flagApply, flagDryRun},
		requiredFlags: []string{"if-match", "apply"},
		wp:            "WP-05",
	},
	{
		name:          "rm",
		summary:       "delete one row",
		usage:         "rm ROW --if-match ETAG --apply",
		roles:         []argRole{argPath},
		flags:         []flagSpec{flagIfMatch, flagApply, flagDryRun},
		requiredFlags: []string{"if-match", "apply"},
		wp:            "WP-05",
	},
	{
		name:          "batch",
		summary:       "apply several mutations in one transaction",
		usage:         "batch --data JSON|-",
		flags:         []flagSpec{flagData, flagDryRun},
		requiredFlags: []string{"data"},
		wp:            "WP-06",
	},
	{
		name:    "export",
		summary: "export rows to stdout as JSON or JSONL",
		usage:   "export PATH [--where EXPR]... [--format json|jsonl] [--limit N] [--cursor TOKEN]",
		roles:   []argRole{argPath},
		flags:   []flagSpec{flagWhere, flagFormat, flagLimit, flagCursor},
		wp:      "WP-03",
	},
}

func findCommand(name string) *commandSpec {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// lookupFlag resolves a flag name against the command's flags then the global
// set. Unknown names are usage errors.
func (s *commandSpec) lookupFlag(name string) (flagSpec, bool) {
	if s != nil {
		for _, f := range s.flags {
			if f.name == name {
				return f, true
			}
		}
	}
	for _, f := range globalFlags {
		if f.name == name {
			return f, true
		}
	}
	return flagSpec{}, false
}
