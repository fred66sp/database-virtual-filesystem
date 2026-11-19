package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"dbvfs/internal/audit"
	"dbvfs/internal/catalog"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/storage/sqlite"
	"dbvfs/internal/vfs"
)

// dryRunWarning is the FR-10 contract: a plan is a snapshot of consultable
// preconditions, never authorization for a later invocation and never a
// promise about constraints or triggers.
const dryRunWarning = "plan only: this is not authorization and does not guarantee a later mutation; constraints and triggers are not evaluated"

// MutationOptions carries the audit identity of one mutation invocation.
type MutationOptions struct {
	RequestID string
	Actor     string
	DryRun    bool
}

// MutationPlan is the --dry-run output. Mutates is always false.
type MutationPlan struct {
	Op          string                   `json:"op"`
	Path        string                   `json:"path"`
	Table       string                   `json:"table"`
	Destination string                   `json:"destination,omitempty"`
	Fields      map[string]storage.Value `json:"fields,omitempty"`
	IfMatch     string                   `json:"if_match,omitempty"`
	DryRun      bool                     `json:"dry_run"`
	Mutates     bool                     `json:"mutates"`
	Warning     string                   `json:"warning"`
}

// Touch inserts a row, returning the effective row with its ETag (FR-06a).
func (s Service) Touch(ctx context.Context, p vfs.Path, data []byte, opts MutationOptions) (any, error) {
	if p.Kind != vfs.KindTable {
		err := fmt.Errorf("%w: touch needs a table path /ALIAS/TABLE", storage.ErrUnsupported)
		s.auditFailure("touch", p, state.Mount{}, opts, err)
		return nil, err
	}
	m, err := s.mount(p.Alias)
	if err != nil {
		return nil, err
	}
	tablePath, err := pathString(p)
	if err != nil {
		return nil, err
	}
	if err := mutationAllowed(m, "touch", tablePath); err != nil {
		_ = s.auditOutcome("touch", p, m, opts, "denied", "E_DENIED")
		return nil, err
	}
	// touch emits the resulting row with its ETag, so it needs a full-row read
	// of the table the way `cat` does (DESIGN §8).
	if !readOpAllowed(m, "cat", tablePath) {
		_ = s.auditOutcome("touch", p, m, opts, "denied", "E_DENIED")
		return nil, policyDenied("touch", "row", tablePath)
	}
	fields, err := parseMutationFields(p, data)
	if err != nil {
		s.auditFailure("touch", p, m, opts, err)
		return nil, err
	}

	st, err := s.open(p.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	if opts.DryRun {
		if err := planTouch(ctx, st, p.Table, fields); err != nil {
			s.auditFailure("touch", p, m, opts, err)
			return nil, err
		}
		_ = s.auditOutcome("touch", p, m, opts, "dry-run", "OK")
		return MutationPlan{Op: "touch", Path: tablePath, Table: p.Table, Fields: fields, DryRun: true, Warning: dryRunWarning}, nil
	}

	return s.execute(ctx, st, "touch", p, nil, m, opts, storage.Mutation{Kind: "touch", Table: p.Table, Fields: fields})
}

// Write updates authorized fields of one row or field, with a mandatory ETag
// precondition (FR-06b, NFR-04).
func (s Service) Write(ctx context.Context, p vfs.Path, data []byte, ifMatch string, opts MutationOptions) (any, error) {
	if p.Kind != vfs.KindRow && p.Kind != vfs.KindField {
		err := fmt.Errorf("%w: write needs a row or field path", storage.ErrUnsupported)
		s.auditFailure("write", p, state.Mount{}, opts, err)
		return nil, err
	}
	m, err := s.mount(p.Alias)
	if err != nil {
		return nil, err
	}
	if m.Mode != "rw" {
		_ = s.auditOutcome("write", p, m, opts, "denied", "E_DENIED")
		return nil, fmt.Errorf("%w: mount %q is read-only", storage.ErrDenied, p.Alias)
	}
	fields, err := parseMutationFields(p, data)
	if err != nil {
		s.auditFailure("write", p, m, opts, err)
		return nil, err
	}
	path, err := pathString(p)
	if err != nil {
		return nil, err
	}
	// Every field needs its own write grant; a grant on a sibling field is not
	// a segment prefix of this one (FR-13).
	if err := authorizeFieldWrites(m, p, fields); err != nil {
		if errors.Is(err, storage.ErrDenied) {
			_ = s.auditOutcome("write", p, m, opts, "denied", "E_DENIED")
		}
		return nil, err
	}
	rowPath, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: p.Table, Key: p.Key})
	if err != nil {
		return nil, err
	}
	if !readOpAllowed(m, "cat", rowPath) {
		_ = s.auditOutcome("write", p, m, opts, "denied", "E_DENIED")
		return nil, policyDenied("write", "row", rowPath)
	}

	st, err := s.open(p.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	if opts.DryRun {
		if err := planWrite(ctx, st, p, fields, ifMatch); err != nil {
			s.auditFailure("write", p, m, opts, err)
			return nil, err
		}
		_ = s.auditOutcome("write", p, m, opts, "dry-run", "OK")
		return MutationPlan{Op: "write", Path: path, Table: p.Table, Fields: fields, IfMatch: ifMatch, DryRun: true, Warning: dryRunWarning}, nil
	}

	key, err := resolveKeyAgainstStore(ctx, st, p)
	if err != nil {
		s.auditFailure("write", p, m, opts, err)
		return nil, err
	}
	return s.execute(ctx, st, "write", p, nil, m, opts, storage.Mutation{Kind: "write", Table: p.Table, Key: key, Fields: fields, IfMatch: ifMatch})
}

// mutationAllowed enforces the two independent gates from DESIGN §9 and D-17:
// the mount must be read-write, and an explicit policy must grant this
// operation on this exact path.
func mutationAllowed(m state.Mount, op, path string) error {
	if m.Mode != "rw" {
		return fmt.Errorf("%w: mount is read-only; operations need a read-write mount with an explicit policy", storage.ErrDenied)
	}
	if !writeOpAllowed(m, op, path) {
		return policyDenied(op, lastSegment(path), path)
	}
	return nil
}

// authorizeFieldWrites requires one write grant per field (FR-13): a grant on a
// sibling field is not a segment prefix of this one. Callers keep their own
// audit record for the denial.
func authorizeFieldWrites(m state.Mount, p vfs.Path, fields map[string]storage.Value) error {
	base, err := pathString(p)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := base
		if p.Kind == vfs.KindRow {
			target, err = pathString(vfs.Path{Kind: vfs.KindField, Alias: p.Alias, Table: p.Table, Key: p.Key, Field: name})
			if err != nil {
				return err
			}
		}
		if err := mutationAllowed(m, "write", target); err != nil {
			return err
		}
	}
	return nil
}

// execute runs one mutation with the audit sequence from DESIGN §11: durable
// attempt, mutation, result. dest is the row a cp or mv creates, so every
// outcome names it; it is nil for the other operations. A failure after commit
// is reported as an unknown outcome so the caller reconciles by reading, never
// by retrying blindly.
func (s Service) execute(ctx context.Context, st *sqlite.Store, op string, p vfs.Path, dest *vfs.Path, m state.Mount, opts MutationOptions, mutation storage.Mutation) (any, error) {
	if err := s.auditRecord(op, p, dest, m, opts, "attempt", ""); err != nil {
		return nil, fmt.Errorf("record audit attempt for %s: %w", op, err)
	}

	var row storage.Row
	writeErr := st.Write(ctx, func(tx storage.Tx) error {
		var err error
		row, err = tx.Apply(ctx, mutation)
		return err
	})
	if writeErr != nil {
		status := "error"
		if errors.Is(writeErr, storage.ErrCommitUnknown) {
			status = "unknown"
		}
		_ = s.auditRecord(op, p, dest, m, opts, status, auditCode(writeErr))
		return nil, writeErr
	}
	if err := s.auditRecord(op, p, dest, m, opts, "ok", "OK"); err != nil {
		return nil, fmt.Errorf("%w: mutation committed but its final audit record failed: %v; read the database instead of retrying", storage.ErrCommitUnknown, err)
	}
	path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: p.Table, Key: row.Key})
	if err != nil {
		return nil, err
	}
	return RowData{Path: path, Fields: fieldMap(row), ETag: row.ETag}, nil
}

// planTouch validates everything a dry-run can consult without writing: the
// table is addressable, has no triggers, the columns exist and the effective
// key is determinable.
func planTouch(ctx context.Context, st *sqlite.Store, table string, fields map[string]storage.Value) error {
	t, err := st.Table(ctx, table)
	if err != nil {
		return err
	}
	if err := writableTable(t); err != nil {
		return err
	}
	if err := planFields(t, fields, true); err != nil {
		return err
	}
	pk := t.PrimaryKey()
	for _, column := range pk {
		if _, ok := fields[column.Name]; ok {
			continue
		}
		if len(pk) == 1 && catalog.Affinity(pk[0].DeclaredType) == "INTEGER" {
			return nil
		}
		return fmt.Errorf("%w: cannot determine the effective key of table %q without its primary key", storage.ErrUnsupported, table)
	}
	return nil
}

// planWrite validates the target row and the ETag precondition by reading them,
// never by writing. Constraint and trigger effects remain unproven, which the
// plan warning states.
func planWrite(ctx context.Context, st *sqlite.Store, p vfs.Path, fields map[string]storage.Value, ifMatch string) error {
	if ifMatch == "" {
		return fmt.Errorf("%w: write requires an ETag precondition", storage.ErrInvalid)
	}
	t, err := st.Table(ctx, p.Table)
	if err != nil {
		return err
	}
	if err := writableTable(t); err != nil {
		return err
	}
	if err := planFields(t, fields, false); err != nil {
		return err
	}
	return st.Read(ctx, func(rd storage.Reader) error {
		key, err := resolveKey(p.Key, t.Table)
		if err != nil {
			return err
		}
		row, err := rd.Get(ctx, t.Name, key)
		if err != nil {
			return err
		}
		if row.ETag != ifMatch {
			return fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
		}
		return nil
	})
}

func writableTable(t catalog.Table) error {
	if !t.Navigable {
		return fmt.Errorf("%w: table %q has no navigable primary key", storage.ErrUnsupported, t.Name)
	}
	if len(t.Triggers) > 0 {
		return fmt.Errorf("%w: table %q has triggers, so writes are denied", storage.ErrUnsupported, t.Name)
	}
	return nil
}

func planFields(t catalog.Table, fields map[string]storage.Value, allowKey bool) error {
	for name := range fields {
		found := false
		for _, c := range t.Columns {
			if c.Name != name {
				continue
			}
			found = true
			if c.Generated {
				return fmt.Errorf("%w: column %q is generated", storage.ErrUnsupported, name)
			}
			if c.PKOrder > 0 && !allowKey {
				return fmt.Errorf("%w: column %q is part of the primary key", storage.ErrUnsupported, name)
			}
		}
		if !found {
			return fmt.Errorf("%w: unknown column %q", storage.ErrInvalid, name)
		}
	}
	return nil
}

// resolveKeyAgainstStore types the path key against the catalog before the
// write transaction starts. The adapter validates it again inside the
// transaction, where it is authoritative.
func resolveKeyAgainstStore(ctx context.Context, st *sqlite.Store, p vfs.Path) (storage.Key, error) {
	t, err := st.Table(ctx, p.Table)
	if err != nil {
		return nil, err
	}
	return resolveKey(p.Key, t.Table)
}

// parseMutationFields reads --data into typed values. A field path takes one
// value; a row or table path takes an object of column to value, so a partial
// record is a map, never an implicit replacement (FR-06b).
func parseMutationFields(p vfs.Path, data []byte) (map[string]storage.Value, error) {
	v, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	if p.Kind == vfs.KindField {
		value, err := mutationValue(v)
		if err != nil {
			return nil, err
		}
		return map[string]storage.Value{p.Field: value}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: --data for a row or table must be a JSON object", storage.ErrInvalid)
	}
	fields := make(map[string]storage.Value, len(obj))
	for name, raw := range obj {
		value, err := mutationValue(raw)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		fields[name] = value
	}
	// An empty object is a valid touch (every column takes its native
	// default), but an empty partial update changes nothing and is a mistake.
	if len(fields) == 0 && p.Kind != vfs.KindTable {
		return nil, fmt.Errorf("%w: --data has no fields", storage.ErrInvalid)
	}
	return fields, nil
}

// mutationValue accepts natural JSON scalars and the typed {kind,data} wrapper.
func mutationValue(v any) (storage.Value, error) {
	if obj, ok := v.(map[string]any); ok {
		return typedValue(obj)
	}
	return jsonValue(v)
}

func typedValue(obj map[string]any) (storage.Value, error) {
	kindRaw, hasKind := obj["kind"]
	dataRaw, hasData := obj["data"]
	if !hasKind || !hasData || len(obj) != 2 {
		return storage.Value{}, fmt.Errorf("%w: a JSON object value must be a {\"kind\",\"data\"} wrapper", storage.ErrInvalid)
	}
	kind, ok := kindRaw.(string)
	if !ok {
		return storage.Value{}, fmt.Errorf("%w: wrapper kind must be a string", storage.ErrInvalid)
	}
	data, ok := dataRaw.(string)
	if !ok {
		return storage.Value{}, fmt.Errorf("%w: wrapper data must be a string", storage.ErrInvalid)
	}
	switch kind {
	case "null":
		if data != "" {
			return storage.Value{}, fmt.Errorf("%w: null wrapper data must be empty", storage.ErrInvalid)
		}
		return storage.Value{Kind: "null"}, nil
	case "text":
		return storage.Value{Kind: "text", Data: data}, nil
	case "integer":
		n, err := strconv.ParseInt(data, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != data {
			return storage.Value{}, fmt.Errorf("%w: integer %q is not canonical decimal", storage.ErrInvalid, data)
		}
		return storage.Value{Kind: "integer", Data: data}, nil
	case "real":
		f, err := strconv.ParseFloat(data, 64)
		if err != nil {
			return storage.Value{}, fmt.Errorf("%w: real %q is not a number", storage.ErrInvalid, data)
		}
		return storage.Value{Kind: "real", Data: strconv.FormatFloat(f, 'g', -1, 64)}, nil
	case "blob":
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return storage.Value{}, fmt.Errorf("%w: blob data is not standard base64", storage.ErrInvalid)
		}
		return storage.Value{Kind: "blob", Data: data}, nil
	}
	return storage.Value{}, fmt.Errorf("%w: wrapper kind %q", storage.ErrInvalid, kind)
}

// decodeJSON rejects malformed JSON, trailing data and duplicate object keys;
// encoding/json would otherwise keep the last duplicate silently (DESIGN §3).
func decodeJSON(data []byte) (any, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: --data is not valid JSON: %v", storage.ErrInvalid, err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: --data has trailing data after one JSON value", storage.ErrInvalid)
	}
	return v, nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkJSON(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: --data has trailing data after one JSON value", storage.ErrInvalid)
	}
	return nil
}

func walkJSON(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: --data is not valid JSON: %v", storage.ErrInvalid, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: --data is not valid JSON: %v", storage.ErrInvalid, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%w: --data object key is not a string", storage.ErrInvalid)
			}
			if seen[key] {
				return fmt.Errorf("%w: --data repeats the key %q", storage.ErrInvalid, key)
			}
			seen[key] = true
			if err := walkJSON(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing brace
		return err
	case '[':
		for dec.More() {
			if err := walkJSON(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing bracket
		return err
	}
	return nil
}

// auditFailure records a rejected mutation best effort: the rejection itself is
// the answer, but the log still shows who tried what.
func (s Service) auditFailure(op string, p vfs.Path, m state.Mount, opts MutationOptions, err error) {
	_ = s.auditOutcome(op, p, m, opts, "error", auditCode(err))
}

// auditOutcome appends one redacted record. An empty AuditDir means audit is
// not wired (unit tests); production always sets it.
func (s Service) auditOutcome(op string, p vfs.Path, m state.Mount, opts MutationOptions, status, code string) error {
	return s.auditRecord(op, p, nil, m, opts, status, code)
}

// auditRecord is auditOutcome plus the destination a cp or mv creates: for cp
// the affected resource is the row that appears, so the log names it too. The
// destination is redacted with the same rule as the source (D-9).
func (s Service) auditRecord(op string, p vfs.Path, dest *vfs.Path, m state.Mount, opts MutationOptions, status, code string) error {
	if s.AuditDir == "" {
		return nil
	}
	resource, table := auditResource(m, p)
	actor := opts.Actor
	if actor == "" {
		actor = "anonymous"
	}
	rec := audit.Record{
		RequestID: opts.RequestID,
		Actor:     actor,
		Operation: op,
		Alias:     p.Alias,
		Table:     table,
		Resource:  resource,
		Status:    status,
		Code:      code,
	}
	if dest != nil {
		destResource, _ := auditResource(m, *dest)
		rec.Dest = destResource
	}
	return audit.Append(s.AuditDir, rec)
}

// auditResource redacts a row identity the actor may not read: the full path is
// kept only when the actor holds a cat grant on the row; otherwise the identity
// is a truncated SHA-256 plus the table name (D-9, DESIGN §11).
func auditResource(m state.Mount, p vfs.Path) (resource, table string) {
	full, err := p.Canonical()
	if err != nil {
		full = mountPath(p.Alias)
	}
	if p.Kind != vfs.KindRow && p.Kind != vfs.KindField {
		return full, ""
	}
	row := vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: p.Table, Key: p.Key}
	rowPath, rowErr := row.Canonical()
	if rowErr == nil && readOpAllowed(m, "cat", rowPath) {
		return full, ""
	}
	if rowErr != nil {
		rowPath = full
	}
	sum := sha256.Sum256([]byte(rowPath))
	return "sha256:" + hex.EncodeToString(sum[:])[:16], p.Table
}

// auditCode mirrors the DESIGN §10 codes for the audit log only. The CLI
// remains the single owner of exit codes and of the client-facing envelope.
func auditCode(err error) string {
	switch {
	case errors.Is(err, storage.ErrDenied):
		return "E_DENIED"
	case errors.Is(err, storage.ErrNotFound):
		return "E_NOT_FOUND"
	case errors.Is(err, storage.ErrConflict):
		return "E_CONFLICT"
	case errors.Is(err, storage.ErrConstraint):
		return "E_CONSTRAINT"
	case errors.Is(err, storage.ErrBusy):
		return "E_BUSY"
	case errors.Is(err, storage.ErrCommitUnknown):
		return "E_COMMIT_UNKNOWN"
	case errors.Is(err, storage.ErrUnsupported):
		return "E_UNSUPPORTED"
	case errors.Is(err, storage.ErrInvalid):
		return "E_USAGE"
	case errors.Is(err, context.DeadlineExceeded):
		return "E_TIMEOUT"
	}
	return "E_INTERNAL"
}
