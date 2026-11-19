// Package cli owns the command contract from DESIGN §3: argument parsing,
// dispatch, help, the JSON envelope and the mapping from errors to exit codes.
package cli

import (
	"fmt"
	"io"
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
	data, err := execute(inv)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	id, err := requestID(inv.requestID)
	if err != nil {
		return fail(stdout, stderr, err)
	}
	if err := writeJSON(stdout, successEnvelope(data, meta{RequestID: id})); err != nil {
		return fail(stdout, stderr, err)
	}
	return 0
}

// execute is the dispatcher. Command behaviour arrives with its work package;
// until then every command answers with the standard error envelope.
func execute(inv *invocation) (any, error) {
	return nil, codedError(CodeUnsupported, "%s is not implemented until %s", inv.spec.name, inv.spec.wp)
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
