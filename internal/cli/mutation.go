package cli

import (
	"context"
	"fmt"
	"io"

	"dbvfs/internal/app"
)

// executeMutation dispatches the two WP-04 vehicle mutations. cp, mv, rm and
// batch stay for WP-05/WP-06.
func executeMutation(inv *invocation, stdin io.Reader) (result, error) {
	svc, err := newService(inv)
	if err != nil {
		return result{}, err
	}
	if err := resolvePath(&svc, inv); err != nil {
		return result{}, err
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
	}
	return result{}, unreachableError(inv.spec.name)
}

// dataPayload reads the JSON once from stdin when the flag is "-" (DESIGN §3).
func dataPayload(inv *invocation, stdin io.Reader) ([]byte, error) {
	if inv.data != "-" {
		return []byte(inv.data), nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read --data from stdin: %w", err)
	}
	return data, nil
}
