package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
)

// dbai is the only door to the data: one db-ai invocation per call, with its
// JSON envelope as the contract. The CLI is executed, never imported.
type dbai struct {
	binary string
}

// call runs db-ai and returns the data member of an ok envelope. A failed
// envelope becomes an *Error with its own code, and any unreadable output
// becomes E_INTERNAL, exactly like the Python adapter.
func (d dbai) call(ctx context.Context, args ...string) (json.RawMessage, error) {
	binary := d.binary
	if binary == "" {
		binary = "db-ai"
	}
	command := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	if command.ProcessState == nil {
		return nil, errorf(codeInternal, "no se pudo ejecutar db-ai: %v", runErr)
	}

	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return nil, errorf(codeInternal, "db-ai devolvió salida no JSON (exit %d)",
			command.ProcessState.ExitCode())
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return nil, &Error{Code: envelope.Error.Code, Message: envelope.Error.Message}
		}
		return nil, errorf(codeInternal, "db-ai falló sin mensaje")
	}
	// db-ai reports failures in the envelope and also exits non-zero; a parsed
	// ok envelope is the contract, so the process status is not inspected.
	_ = runErr
	return envelope.Data, nil
}
