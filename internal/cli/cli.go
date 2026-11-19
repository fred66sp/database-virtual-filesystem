// Package cli owns the command contract from DESIGN §3: argument parsing,
// dispatch, help, the JSON envelope and the mapping from errors to exit codes.
package cli

import (
	"context"
	"fmt"
	"io"

	"dbvfs/internal/app"
	"dbvfs/internal/state"
)

// Run handles exactly one invocation: parse, dispatch and render. It is the
// only place that writes stdout/stderr and the only place that decides the
// process exit status; main just forwards it to os.Exit.
func Run(args []string, stdout, stderr io.Writer) int {
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
	res, err := execute(inv)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	id, err := requestID(inv.requestID)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	if err := writeJSON(stdout, successEnvelope(res.data, meta{Truncated: res.truncated, RequestID: id})); err != nil {
		return fail(stdout, stderr, err)
	}
	return 0
}

// result is what a handler returns to the envelope: data plus the pagination
// flag meta.truncated.
type result struct {
	data      any
	truncated bool
}

// execute is the dispatcher. Commands implemented by a later work package keep
// answering with the standard error envelope.
func execute(inv *invocation) (result, error) {
	switch inv.spec.name {
	case "mount", "umount", "mounts", "ls", "tree", "stat", "describe", "cat":
		return executeRead(inv)
	}
	return result{}, codedError(CodeUnsupported, "%s is not implemented until %s", inv.spec.name, inv.spec.wp)
}

// executeRead wires the read side: config directory, operation deadline and
// the application service. Mutations and sessions arrive in later packages.
func executeRead(inv *invocation) (result, error) {
	dir, err := state.Dir()
	if err != nil {
		return result{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), inv.timeout)
	defer cancel()

	svc := app.Service{ConfigDir: dir, Timeout: inv.timeout}
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
			return result{}, codedError(CodeUnsupported, "pagination cursors are not implemented until WP-03")
		}
		limit := inv.limit
		if limit == 0 {
			limit = maxLimit
		}
		data, truncated, err := svc.LS(ctx, *inv.path, limit)
		return result{data: data, truncated: truncated}, err
	case "tree":
		limit := inv.limit
		if limit == 0 {
			limit = maxLimit
		}
		data, truncated, err := svc.Tree(ctx, *inv.path, inv.depth, limit)
		return result{data: data, truncated: truncated}, err
	case "stat":
		data, err := svc.Stat(ctx, *inv.path)
		return result{data: data}, err
	case "describe":
		data, err := svc.Describe(ctx, *inv.path)
		return result{data: data}, err
	case "cat":
		data, err := svc.Cat(ctx, *inv.path)
		return result{data: data}, err
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
