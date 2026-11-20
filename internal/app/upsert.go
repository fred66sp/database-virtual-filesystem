package app

import (
	"context"
	"errors"
	"fmt"

	"dbvfs/internal/catalog"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/storage/sqlite"
	"dbvfs/internal/vfs"
)

// UpsertResult is the explicit branch outcome of REF-04: the caller never has
// to infer from an error whether the row was created or updated. Applied
// distinguishes a dry-run (false) from the executed operation (true).
// ETag is the ETag of the resulting row; a dry-run omits it because the
// effective values (defaults, generated columns) are only known after writing.
type UpsertResult struct {
	Result  string `json:"result"`
	Applied bool   `json:"applied"`
	Path    string `json:"path"`
	ETag    string `json:"etag,omitempty"`
}

// Upsert creates or updates the row identified by the primary key carried in
// --data. The branch is resolved inside the BEGIN IMMEDIATE transaction, so the
// grant that is enforced is the one for the branch actually taken: touch for an
// insert and write for an update (REF-04, DESIGN §8).
func (s Service) Upsert(ctx context.Context, p vfs.Path, data []byte, ifMatch string, opts MutationOptions) (any, error) {
	if p.Kind != vfs.KindTable {
		err := fmt.Errorf("%w: upsert needs a table path /ALIAS/TABLE", storage.ErrUnsupported)
		s.auditFailure("upsert", p, state.Mount{}, opts, err)
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
	fields, err := parseMutationFields(p, data)
	if err != nil {
		s.auditFailure("upsert", p, m, opts, err)
		return nil, err
	}

	st, err := s.open(p.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	t, err := st.Table(ctx, p.Table)
	if err != nil {
		s.auditFailure("upsert", p, m, opts, err)
		return nil, err
	}
	if err := writableTable(t); err != nil {
		s.auditFailure("upsert", p, m, opts, err)
		return nil, err
	}
	key, err := upsertKey(t, fields)
	if err != nil {
		s.auditFailure("upsert", p, m, opts, err)
		return nil, err
	}
	row := vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: p.Table, Key: key}
	rowPath, err := pathString(row)
	if err != nil {
		return nil, err
	}
	update := nonKeyFields(t, fields)

	if opts.DryRun {
		return s.planUpsert(ctx, st, m, p, row, rowPath, tablePath, update, key, ifMatch, opts)
	}

	var (
		result    UpsertResult
		branch    string
		attempted bool
	)
	writeErr := st.Write(ctx, func(tx storage.Tx) error {
		b, current, err := upsertBranch(ctx, tx, p.Table, key)
		if err != nil {
			return err
		}
		branch = b
		if err := authorizeUpsert(m, p.Table, row, rowPath, tablePath, update, ifMatch, b, current); err != nil {
			return err
		}
		// The attempt record is durable and branch-specific; if it cannot be
		// written the transaction rolls back before any SQL runs (DESIGN §11).
		if err := s.auditRecord(b, row, nil, m, opts, "attempt", ""); err != nil {
			return fmt.Errorf("record audit attempt for %s: %w", b, err)
		}
		attempted = true

		stored, err := tx.Apply(ctx, storage.Mutation{
			Kind: "upsert", Table: p.Table, Key: key, Fields: fields, IfMatch: ifMatch,
		})
		if err != nil {
			return err
		}
		result = UpsertResult{Result: b, Applied: true, Path: rowPath, ETag: stored.ETag}
		return nil
	})
	if writeErr != nil {
		if !attempted {
			_ = s.auditRecord(auditBranch(branch), row, nil, m, opts, failureStatus(writeErr), auditCode(writeErr))
		}
		return nil, writeErr
	}
	if err := s.auditRecord(result.Result, row, nil, m, opts, "ok", "OK"); err != nil {
		return nil, fmt.Errorf("%w: mutation committed but its final audit record failed: %v; read the database instead of retrying", storage.ErrCommitUnknown, err)
	}
	return result, nil
}

// planUpsert is the dry-run: it resolves the branch, enforces that branch's
// grant and the ETag precondition, and writes nothing. Its ETag is omitted
// because defaults and generated columns only materialize on a real insert.
func (s Service) planUpsert(ctx context.Context, st *sqlite.Store, m state.Mount, p vfs.Path, row vfs.Path, rowPath, tablePath string, update map[string]storage.Value, key storage.Key, ifMatch string, opts MutationOptions) (any, error) {
	var (
		result UpsertResult
		branch string
	)
	err := st.Read(ctx, func(rd storage.Reader) error {
		b, current, err := upsertBranch(ctx, rd, p.Table, key)
		if err != nil {
			return err
		}
		branch = b
		if err := authorizeUpsert(m, p.Table, row, rowPath, tablePath, update, ifMatch, b, current); err != nil {
			return err
		}
		_ = s.auditRecord(b, row, nil, m, opts, "dry-run", "OK")
		result = UpsertResult{Result: b, Applied: false, Path: rowPath}
		return nil
	})
	if err != nil {
		_ = s.auditRecord(auditBranch(branch), row, nil, m, opts, failureStatus(err), auditCode(err))
		return nil, err
	}
	return result, nil
}

// upsertKey reads the conflict destination from --data. The destination is the
// primary key: a table without one has no row path for the response and no
// per-field write grant, so upsert is E_UNSUPPORTED there, exactly as touch and
// write are (D-12). Other UNIQUE constraints are never a destination.
func upsertKey(t catalog.Table, fields map[string]storage.Value) (storage.Key, error) {
	pk := t.PrimaryKey()
	if len(pk) == 0 {
		return nil, fmt.Errorf("%w: table %q has no primary key, so it has no stable upsert identity", storage.ErrUnsupported, t.Name)
	}
	key := make(storage.Key, 0, len(pk))
	for _, column := range pk {
		value, ok := fields[column.Name]
		if !ok {
			return nil, fmt.Errorf("%w: upsert needs every primary-key column in --data; %q is missing", storage.ErrInvalid, column.Name)
		}
		if value.Kind != "text" && value.Kind != "integer" {
			return nil, fmt.Errorf("%w: primary key column %q is not navigable", storage.ErrUnsupported, column.Name)
		}
		key = append(key, value)
	}
	return key, nil
}

// nonKeyFields is the partial record the update branch may write: the identity
// is not updated and generated columns are rejected later by the adapter.
func nonKeyFields(t catalog.Table, fields map[string]storage.Value) map[string]storage.Value {
	out := make(map[string]storage.Value, len(fields))
	for name, value := range fields {
		column, ok := findCatalogColumn(t, name)
		if !ok || column.PKOrder > 0 || column.Generated {
			continue
		}
		out[name] = value
	}
	return out
}

func findCatalogColumn(t catalog.Table, name string) (storage.Column, bool) {
	for _, column := range t.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return storage.Column{}, false
}

// upsertBranch reads the target inside the caller's transaction: a present row
// is an update, a missing one an insert. Any other error is a real failure.
func upsertBranch(ctx context.Context, rd storage.Reader, table string, key storage.Key) (string, storage.Row, error) {
	current, err := rd.Get(ctx, table, key)
	if errors.Is(err, storage.ErrNotFound) {
		return "created", storage.Row{}, nil
	}
	if err != nil {
		return "", storage.Row{}, err
	}
	return "updated", current, nil
}

// authorizeUpsert enforces the grant of the branch actually taken: touch for an
// insert and write for an update, with the same reads, denials and ETag rules
// those operations already require. The absence of --if-match never authorizes
// an unconditional update, and --if-match on a missing row never recreates it.
func authorizeUpsert(m state.Mount, table string, row vfs.Path, rowPath, tablePath string, update map[string]storage.Value, ifMatch, branch string, current storage.Row) error {
	if branch == "created" {
		if err := mutationAllowed(m, "touch", tablePath); err != nil {
			return err
		}
		if !readOpAllowed(m, "cat", tablePath) {
			return policyDenied("touch", "row", tablePath)
		}
		if ifMatch != "" {
			return fmt.Errorf("%w: --if-match was given but table %q has no row with that key; omit --if-match to create it", storage.ErrInvalid, table)
		}
		return nil
	}

	if len(update) == 0 {
		return fmt.Errorf("%w: upsert of an existing row has no non-key column to update", storage.ErrInvalid)
	}
	if err := authorizeFieldWrites(m, row, update); err != nil {
		return err
	}
	if !readOpAllowed(m, "cat", rowPath) {
		return policyDenied("write", "row", rowPath)
	}
	if ifMatch == "" {
		return fmt.Errorf("%w: upsert of an existing row requires --if-match with its current ETag", storage.ErrInvalid)
	}
	if ifMatch != current.ETag {
		return fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, table)
	}
	return nil
}

// auditBranch names the audited operation: the effective branch once known, or
// the generic command name for failures raised before the branch is resolved.
func auditBranch(branch string) string {
	if branch == "" {
		return "upsert"
	}
	return branch
}

func failureStatus(err error) string {
	switch {
	case errors.Is(err, storage.ErrDenied):
		return "denied"
	case errors.Is(err, storage.ErrCommitUnknown):
		return "unknown"
	}
	return "error"
}
