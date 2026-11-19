package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"

	"dbvfs/internal/app"
)

// apiVersion is the version of the JSON envelope from DESIGN §10.
const apiVersion = 1

// maxOutputBytes caps one invocation's stdout (NFR-05: 16 MiB).
const maxOutputBytes = 16 << 20

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
	if len(b)+1 > maxOutputBytes {
		return limitError()
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// writeJSONL emits one row object per line (FR-16). A page that is truncated
// ends with one trailer line carrying meta.next_cursor, so a stream consumer
// can continue without changing the row shape.
func writeJSONL(w io.Writer, rows []app.RowData, m meta) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
		if buf.Len() > maxOutputBytes {
			return limitError()
		}
	}
	if m.Truncated || m.NextCursor != "" {
		if err := enc.Encode(struct {
			Meta meta `json:"meta"`
		}{m}); err != nil {
			return err
		}
	}
	if buf.Len() > maxOutputBytes {
		return limitError()
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func limitError() error {
	return codedError(CodeLimit, "output exceeds the %d MiB limit", maxOutputBytes>>20)
}
