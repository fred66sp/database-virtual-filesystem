package cli

import (
	"context"
	"fmt"
	"io"

	"dbvfs/internal/app"
)

// executeMutation dispatches touch, write, cp, mv, rm and batch.
func executeMutation(inv *invocation, stdin io.Reader) (result, error) {
	// D-7: only mv and rm need --apply. Without it they may still plan with
	// --dry-run; otherwise the invocation is incomplete, not executed.
	if (inv.spec.name == "mv" || inv.spec.name == "rm") && !inv.apply && !inv.dryRun {
		return result{}, usageError("%s requires %q", inv.spec.name, "--apply")
	}
	svc, err := newService(inv)
	if err != nil {
		return result{}, err
	}
	if err := resolvePath(&svc, inv); err != nil {
		return result{}, err
	}
	if inv.destRelative {
		dest, err := svc.Resolve(inv.session, inv.destRaw)
		if err != nil {
			return result{}, err
		}
		inv.dest = &dest
	}
	payload, err := dataPayload(inv, stdin)
	if err != nil {
		return result{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), inv.timeout)
	defer cancel()

	opts := app.MutationOptions{RequestID: inv.requestID, Actor: inv.actor, DryRun: inv.dryRun}
	switch inv.spec.name {
	case "touch":
		data, err := svc.Touch(ctx, *inv.path, payload, opts)
		return result{data: data}, err
	case "write":
		data, err := svc.Write(ctx, *inv.path, payload, inv.ifMatch, opts)
		return result{data: data}, err
	case "cp", "mv":
		var data any
		if inv.spec.name == "cp" {
			data, err = svc.Copy(ctx, *inv.path, *inv.dest, inv.ifMatch, opts)
		} else {
			data, err = svc.Move(ctx, *inv.path, *inv.dest, inv.ifMatch, opts)
		}
		return result{data: data}, err
	case "rm":
		data, err := svc.Remove(ctx, *inv.path, inv.ifMatch, opts)
		return result{data: data}, err
	case "batch":
		data, err := svc.Batch(ctx, payload, opts)
		return result{data: data}, err
	}
	return result{}, unreachableError(inv.spec.name)
}

// maxStdinPayload caps --data read from stdin. D-8 allows 100 operations and a
// typical batch is far below 1 MiB; 4 MiB leaves headroom without letting a
// runaway pipe exhaust memory before the D-8 check runs.
const maxStdinPayload = 4 << 20

// dataPayload reads the JSON once from stdin when the flag is "-" (DESIGN §3).
func dataPayload(inv *invocation, stdin io.Reader) ([]byte, error) {
	if inv.data != "-" {
		return []byte(inv.data), nil
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxStdinPayload+1))
	if err != nil {
		return nil, fmt.Errorf("read --data from stdin: %w", err)
	}
	if len(data) > maxStdinPayload {
		return nil, codedError(CodeLimit, "--data from stdin exceeds %d bytes", maxStdinPayload)
	}
	return data, nil
}
