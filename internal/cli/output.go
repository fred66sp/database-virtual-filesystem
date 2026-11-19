package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
)

// apiVersion is the version of the JSON envelope from DESIGN §10.
const apiVersion = 1

// successResponse and errorResponse are the two documented envelope shapes.
// They are separate types so neither can leak the other's fields: an error
// carries only code and message, a success always carries data and meta.
type successResponse struct {
	Version int  `json:"version"`
	OK      bool `json:"ok"`
	Data    any  `json:"data"`
	Meta    meta `json:"meta"`
}

type errorResponse struct {
	Version int       `json:"version"`
	OK      bool      `json:"ok"`
	Error   errorBody `json:"error"`
}

// meta carries pagination and request metadata. It has no timestamps, so reads
// stay deterministic (NFR-01).
type meta struct {
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
	RequestID  string `json:"request_id"`
}

// errorBody is the stable error payload. code is what the agent branches on.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func successEnvelope(data any, m meta) successResponse {
	return successResponse{Version: apiVersion, OK: true, Data: data, Meta: m}
}

func errorEnvelope(err *Error) errorResponse {
	return errorResponse{
		Version: apiVersion,
		OK:      false,
		Error:   errorBody{Code: string(err.Code), Message: err.Message},
	}
}

// requestID returns the client-supplied ID or generates one, as required by
// DESIGN §10: meta.request_id is always present.
func requestID(provided string) (string, error) {
	if provided != "" {
		return provided, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// writeJSON emits exactly one compact JSON object followed by a newline.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
