// Command acm is the ACM memory CLI: it opens or restores a session with its
// continuity Markdown and serves the AIF 0.2 operations over db-ai.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	interaction "github.com/fred66sp/agent-interaction-forms"
	"github.com/fred66sp/database-virtual-filesystem/acm/internal/aifops"
	"github.com/fred66sp/database-virtual-filesystem/acm/internal/memory"
)

const version = "0.1.0"

const usageText = `acm — memoria de continuidad sobre db-ai

Uso:
  acm start [--goal G] [--session S]   abre o restaura la sesión y muestra el contexto
  acm aif                              sirve una petición AIF 0.2 por la entrada estándar
  acm --version                        muestra la versión

Identidad: --session o OPENCODE_SESSION_ID. Montaje: ACM_ALIAS (mem);
agente: ACM_AGENT_ID (opencode). db-ai debe estar en PATH.
`

// startExitCodes is the code the old adapter gave each contract error. It is
// kept so a consumer of `acm start` keeps its exit-code switch.
var startExitCodes = map[string]int{
	"E_USAGE": 2, "E_VALIDATION": 2, "E_NOT_FOUND": 3, "E_STALE_SESSION": 3,
	"E_DENIED": 4, "E_CONFLICT": 5, "E_CONSTRAINT": 6, "E_BUSY": 7,
	"E_TIMEOUT": 7, "E_UNSUPPORTED": 8, "E_LIMIT": 8, "E_INTERNAL": 9,
	"E_COMMIT_UNKNOWN": 10,
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// run is the whole CLI, testable without launching the process.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer,
	getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintf(stdout, "acm %s\n", version)
		return 0
	case "--help", "-h", "-help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "start":
		return runStart(ctx, args[1:], stdout, stderr, getenv)
	case "aif":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "acm aif: no admite argumentos\n")
			return 2
		}
		return runAIF(ctx, stdin, stdout, stderr, getenv)
	default:
		fmt.Fprintf(stderr, "acm: comando desconocido %q\n", args[0])
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

func newStore(getenv func(string) string) *memory.Store {
	return memory.New(getenv("ACM_ALIAS"), getenv("ACM_AGENT_ID"), getenv)
}

func runStart(ctx context.Context, args []string, stdout, stderr io.Writer,
	getenv func(string) string) int {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	goal := flags.String("goal", "", "objetivo de la sesión")
	session := flags.String("session", "", "identificador de sesión")
	if err := flags.Parse(args); err != nil {
		return startFailure(stdout, &memory.Error{Code: "E_USAGE", Message: err.Error()})
	}
	if flags.NArg() > 0 {
		return startFailure(stdout, &memory.Error{
			Code: "E_USAGE", Message: "start no admite argumentos posicionales"})
	}
	store := newStore(getenv)
	resolved, err := store.ResolveSession(*session)
	if err != nil {
		return startFailure(stdout, err)
	}
	markdown, err := store.Start(ctx, resolved, *goal)
	if err != nil {
		return startFailure(stdout, err)
	}
	fmt.Fprintln(stdout, markdown)
	return 0
}

// startFailure answers the JSON envelope the Python adapter printed and maps
// its code to the same exit code.
func startFailure(stdout io.Writer, err error) int {
	code := "E_INTERNAL"
	var contract *memory.Error
	if errors.As(err, &contract) {
		code = contract.Code
	}
	_ = writeJSON(stdout, struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}{false, code, err.Error()})
	exit, ok := startExitCodes[code]
	if !ok {
		return 1
	}
	return exit
}

// runAIF reads one request, answers one object and returns the exit code the
// AIF CLI profile fixes for its result.
func runAIF(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer,
	getenv func(string) string) int {
	raw, err := io.ReadAll(io.LimitReader(stdin, interaction.MaxInputBytes+1))
	if err != nil {
		return writeAIF(stdout, stderr, &interaction.Response{
			AIF: interaction.Version, Result: interaction.ResultFailed,
			Message: "no se pudo leer la petición", Code: "stdin_unavailable",
		})
	}
	return writeAIF(stdout, stderr, aifops.New(ctx, newStore(getenv)).Handle(raw))
}

func writeAIF(stdout, stderr io.Writer, response *interaction.Response) int {
	if err := writeJSON(stdout, response); err != nil {
		if errors.Is(err, syscall.EPIPE) {
			return 0
		}
		fmt.Fprintf(stderr, "acm: no se pudo escribir la respuesta: %v\n", err)
		return 1
	}
	return exitCodeOf(response)
}

func exitCodeOf(response *interaction.Response) int {
	if response.Discovery != nil {
		return 0
	}
	switch response.Result {
	case interaction.ResultExecuted:
		return 0
	case interaction.ResultInputNeeded, interaction.ResultInvalid:
		return 2
	case interaction.ResultDenied:
		return 4
	case interaction.ResultFailed:
		return 1
	default:
		return 3
	}
}

// writeJSON writes one compact JSON document with the same escaping Python's
// json.dumps(ensure_ascii=False) uses for the members it controls.
func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
