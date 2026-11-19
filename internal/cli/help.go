package cli

import (
	"fmt"
	"io"
	"strings"
)

const exitCodesHelp = `Exit codes:
  0    OK
  2    E_USAGE
  3    E_NOT_FOUND or E_STALE_SESSION
  4    E_DENIED
  5    E_CONFLICT
  6    E_CONSTRAINT
  7    E_BUSY or E_TIMEOUT
  8    E_UNSUPPORTED or E_LIMIT
  9    E_INTERNAL
  10   E_COMMIT_UNKNOWN
`

// writeHelp renders deterministic help text. Help never touches storage, so it
// works without a mounted database (DESIGN §3).
func writeHelp(w io.Writer, spec *commandSpec) error {
	var b strings.Builder
	if spec == nil {
		writeRootHelp(&b)
	} else {
		fmt.Fprintf(&b, "%s — %s\n\nUsage:\n  db-ai %s\n\n", spec.name, spec.summary, spec.usage)
		if len(spec.flags) > 0 {
			b.WriteString("Flags:\n")
			writeFlagList(&b, spec.flags)
			b.WriteString("\n")
		}
		b.WriteString("Global flags:\n")
		writeFlagList(&b, globalFlags)
		b.WriteString("\n")
		b.WriteString(exitCodesHelp)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeRootHelp(b *strings.Builder) {
	b.WriteString("db-ai — SQLite databases as a virtual filesystem\n\n")
	b.WriteString("Usage:\n  db-ai <command> [flags] [arguments]\n  db-ai --help\n\n")
	b.WriteString("Commands:\n")
	for _, spec := range commands {
		fmt.Fprintf(b, "  %s\n      %s\n", spec.usage, spec.summary)
	}
	b.WriteString("\nGlobal flags:\n")
	writeFlagList(b, globalFlags)
	b.WriteString("\n")
	b.WriteString(`Output:
  Exactly one JSON object per invocation on stdout:
    success: {"version":1,"ok":true,"data":...,"meta":{"truncated":false,"request_id":"..."}}
    failure: {"version":1,"ok":false,"error":{"code":"E_...","message":"..."}}
  Diagnostics go to stderr.

`)
	b.WriteString(exitCodesHelp)
	b.WriteString(`
Examples:
  db-ai mount ./tasks.db /work
  db-ai ls /work
  db-ai cat /work/tasks/TK-001
  db-ai find /work/tasks --where 'status=pending' --limit 20
`)
}

func writeFlagList(b *strings.Builder, flags []flagSpec) {
	labels := make([]string, len(flags))
	width := 0
	for i, f := range flags {
		labels[i] = "--" + f.name
		if f.arg != "" {
			labels[i] += " " + f.arg
		}
		width = max(width, len(labels[i]))
	}
	for i, f := range flags {
		fmt.Fprintf(b, "  %-*s  %s\n", width, labels[i], f.doc)
	}
}
