// Package cli owns the command contract from DESIGN §3: argument parsing,
// dispatch, help, the JSON envelope and the mapping from errors to exit codes.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"dbvfs/internal/app"
	"dbvfs/internal/state"
)

// Run handles exactly one invocation: parse, dispatch and render. It is the
// only place that writes stdout/stderr and the only place that decides the
// process exit status; main just forwards it to os.Exit.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, os.Stdin, stdout, stderr)
}

// run is Run with an injected stdin, so `--data -` is testable without a TTY.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	inv, err := parse(args)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	if inv.help {
		// If the help write fails mid-stream, fail() would append an error
		// envelope to the same stdout; unreachable on real stdout (SIGPIPE
		// kills the process first), tolerated for the io.Writer abstraction.
		if err := writeHelp(stdout, inv.spec); err != nil {
			return fail(stdout, stderr, err)
		}
		return 0
	}
	// The request ID identifies the audit records too, so it is resolved before
	// dispatch, not only in the response envelope (DESIGN §10, §11).
	id, err := requestID(inv.requestID)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	inv.requestID = id
	res, err := execute(inv, stdin)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	m := meta{Truncated: res.truncated, NextCursor: res.nextCursor, RequestID: id}
	if res.jsonl != nil {
		if err := writeJSONL(stdout, res.jsonl, m); err != nil {
			return fail(stdout, stderr, err)
		}
		return 0
	}
	if err := writeJSON(stdout, successEnvelope(res.data, m)); err != nil {
		return fail(stdout, stderr, err)
	}
	return 0
}

// result is what a handler returns to the renderer: data plus pagination
// metadata. A non-nil jsonl slice selects JSONL output (export --format jsonl).
type result struct {
	data       any
	truncated  bool
	nextCursor string
	jsonl      []app.RowData
}

// execute is the dispatcher. Commands implemented by a later work package keep
// answering with the standard error envelope.
func execute(inv *invocation, stdin io.Reader) (result, error) {
	switch inv.spec.name {
	case "version":
		// Build metadata only: no config, state or database is touched.
		return result{data: versionInfo()}, nil
	case "cd", "pwd":
		return executeSession(inv)
	case "touch", "write", "upsert", "cp", "mv", "rm", "batch":
		return executeMutation(inv, stdin)
	case "mount", "umount", "mounts", "ls", "tree", "stat", "describe", "cat",
		"find", "grep", "export":
		return executeRead(inv)
	}
	return result{}, codedError(CodeUnsupported, "%s is not implemented until %s", inv.spec.name, inv.spec.wp)
}

// newService wires the config directory, the sessions and audit directories and
// the operation deadline into the application layer.
func newService(inv *invocation) (app.Service, error) {
	dir, err := state.Dir()
	if err != nil {
		return app.Service{}, err
	}
	sessions, err := state.SessionsDir()
	if err != nil {
		return app.Service{}, err
	}
	auditDir, err := state.StateDir()
	if err != nil {
		return app.Service{}, err
	}
	return app.Service{ConfigDir: dir, StateDir: sessions, AuditDir: auditDir, Timeout: inv.timeout}, nil
}

// resolvePath turns a deferred relative path into a VFS path using the
// session's working directory. The application owns session files, so the CLI
// never guesses a cwd.
func resolvePath(svc *app.Service, inv *invocation) error {
	if !inv.pathRelative && !inv.pathFromCwd {
		return nil
	}
	p, err := svc.Resolve(inv.session, inv.pathRaw)
	if err != nil {
		return err
	}
	inv.path = &p
	return nil
}

func effectiveLimit(limit int) int {
	if limit <= 0 {
		return maxLimit
	}
	return limit
}

// executeRead wires the read side: config directory, operation deadline and
// the application service.
func executeRead(inv *invocation) (result, error) {
	svc, err := newService(inv)
	if err != nil {
		return result{}, err
	}
	if err := resolvePath(&svc, inv); err != nil {
		return result{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), inv.timeout)
	defer cancel()

	switch inv.spec.name {
	case "mount":
		info, err := svc.Mount(inv.args[0], inv.alias, inv.mode)
		return result{data: info}, err
	case "umount":
		info, err := svc.Umount(inv.alias)
		return result{data: info}, err
	case "mounts":
		mounts, err := svc.Mounts()
		if err != nil {
			return result{}, err
		}
		return result{data: mountsData{Mounts: mounts}}, nil
	case "ls":
		if inv.cursor != "" {
			return result{}, codedError(CodeUnsupported, "ls does not support pagination cursors")
		}
		data, truncated, err := svc.LS(ctx, *inv.path, effectiveLimit(inv.limit))
		return result{data: data, truncated: truncated}, err
	case "tree":
		data, truncated, err := svc.Tree(ctx, *inv.path, inv.depth, effectiveLimit(inv.limit))
		return result{data: data, truncated: truncated}, err
	case "stat":
		data, err := svc.Stat(ctx, *inv.path)
		return result{data: data}, err
	case "describe":
		data, err := svc.Describe(ctx, *inv.path)
		return result{data: data}, err
	case "cat":
		data, err := svc.Cat(ctx, *inv.path, inv.fields)
		return result{data: data}, err
	case "find":
		if inv.count {
			// A count has no row shape, so --fields, --limit and --order do not
			// apply to the result; the grammar of a given --order is still
			// validated inside Count so that a typo cannot hide behind an
			// ignored flag. The count itself stays order-independent.
			data, err := svc.Count(ctx, *inv.path, "find", inv.where, inv.order, inv.fields, nil)
			return result{data: data}, err
		}
		data, truncated, next, err := svc.Find(ctx, *inv.path, inv.where, inv.fields, inv.order, effectiveLimit(inv.limit), inv.cursor)
		return result{data: data, truncated: truncated, nextCursor: next}, err
	case "grep":
		if inv.count {
			data, err := svc.Count(ctx, *inv.path, "grep", nil, "", nil, &inv.args[1])
			return result{data: data}, err
		}
		data, truncated, next, err := svc.Grep(ctx, *inv.path, inv.args[1], effectiveLimit(inv.limit), inv.cursor)
		return result{data: data, truncated: truncated, nextCursor: next}, err
	case "export":
		rows, truncated, next, err := svc.Export(ctx, *inv.path, inv.where, effectiveLimit(inv.limit), inv.cursor)
		if err != nil {
			return result{}, err
		}
		if inv.format == "jsonl" {
			return result{jsonl: rows, truncated: truncated, nextCursor: next}, nil
		}
		path, err := inv.path.Canonical()
		if err != nil {
			return result{}, err
		}
		return result{data: app.FindData{Path: path, Rows: rows}, truncated: truncated, nextCursor: next}, nil
	}
	return result{}, unreachableError(inv.spec.name)
}

// executeSession handles the two commands that read or write session state.
func executeSession(inv *invocation) (result, error) {
	svc, err := newService(inv)
	if err != nil {
		return result{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), inv.timeout)
	defer cancel()

	switch inv.spec.name {
	case "cd":
		if err := resolvePath(&svc, inv); err != nil {
			return result{}, err
		}
		info, err := svc.CD(ctx, inv.session, *inv.path)
		return result{data: info}, err
	case "pwd":
		info, err := svc.PWD(ctx, inv.session)
		return result{data: info}, err
	}
	return result{}, unreachableError(inv.spec.name)
}

// mountsData is the mounts command shape: a deterministic list, never null.
type mountsData struct {
	Mounts []app.MountInfo `json:"mounts"`
}

func unreachableError(name string) error {
	return codedError(CodeInternal, "no handler for %s", name)
}

func fail(stdout, stderr io.Writer, err error) int {
	coded := CodedError(err)
	if coded.Code == CodeInternal && coded.Err != nil {
		diagnostic(stderr, coded.Err)
	}
	if werr := writeJSON(stdout, errorEnvelope(coded)); werr != nil {
		// Any write failure, EPIPE included, leaves the exit non-zero: the
		// invocation failed even if the envelope could not be delivered (§10).
		diagnostic(stderr, fmt.Errorf("writing stdout: %w", werr))
	}
	return coded.Code.Exit()
}

func diagnostic(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "db-ai: %v\n", err) // diagnostics are best effort
}
