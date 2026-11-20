package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"

	"dbvfs/internal/catalog"
	"dbvfs/internal/storage"
)

// SQLite primary result codes, masked out of any extended code.
const (
	sqliteBusy       = 5
	sqliteLocked     = 6
	sqliteReadOnly   = 8
	sqliteConstraint = 19
)

// classify keeps engine failures in the storage port's vocabulary: lock
// contention, constraint violations and read-only files get a sentinel the CLI
// can map to E_BUSY, E_CONSTRAINT and E_DENIED instead of an opaque internal
// error (DESIGN §10).
func classify(err error) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() & 0xff {
	case sqliteBusy, sqliteLocked:
		return fmt.Errorf("%w: %v", storage.ErrBusy, err)
	case sqliteConstraint:
		return fmt.Errorf("%w: %v", storage.ErrConstraint, err)
	case sqliteReadOnly:
		return fmt.Errorf("%w: %v", storage.ErrDenied, err)
	}
	return err
}

// Apply executes one mutation inside the caller's transaction. The catalog is
// the authority: unknown, generated and primary-key columns are rejected here,
// and the ETag comparison happens against the row as read inside this same
// transaction (DESIGN §8, NFR-04).
func (r *reader) Apply(ctx context.Context, m storage.Mutation) (storage.Row, error) {
	t, err := r.table(m.Table)
	if err != nil {
		return storage.Row{}, err
	}
	if !t.Navigable {
		return storage.Row{}, fmt.Errorf("%w: table %q has no navigable primary key", storage.ErrUnsupported, m.Table)
	}
	// DESIGN §8 rejects write triggers on the target table in the MVP, even
	// ones that look harmless: their effects are not part of the plan.
	if len(t.Triggers) > 0 {
		return storage.Row{}, fmt.Errorf("%w: table %q has triggers, so writes are denied", storage.ErrUnsupported, m.Table)
	}
	if m.Kind != "upsert" {
		if err := r.denyCascades(t, m.Kind); err != nil {
			return storage.Row{}, err
		}
	}
	switch m.Kind {
	case "touch":
		return r.insert(ctx, t, m.Fields)
	case "write":
		return r.update(ctx, t, m.Key, m.Fields, m.IfMatch)
	case "upsert":
		return r.upsert(ctx, t, m.Key, m.Fields, m.IfMatch)
	case "cp":
		return r.copyRow(ctx, t, m.Key, m.Destination, m.IfMatch)
	case "mv":
		return r.rename(ctx, t, m.Key, m.Destination, m.IfMatch)
	case "rm":
		return r.remove(ctx, t, m.Key, m.IfMatch)
	}
	return storage.Row{}, fmt.Errorf("%w: mutation kind %q", storage.ErrUnsupported, m.Kind)
}

// requireAbsentDestination is the cp/mv target check (FR-07, FR-09): an
// existing destination is a conflict, never an overwrite.
func (r *reader) requireAbsentDestination(ctx context.Context, t *catalog.Table, dest storage.Key) error {
	if _, err := r.Get(ctx, t.Name, dest); err == nil {
		return fmt.Errorf("%w: destination row already exists in table %q", storage.ErrConflict, t.Name)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return nil
}

// copyRow is `cp` (FR-07): INSERT of the source's ordinary columns under an
// explicit destination key. The ETag precondition belongs to the source row
// (D-6); an existing destination is a conflict, never an overwrite.
func (r *reader) copyRow(ctx context.Context, t *catalog.Table, source, dest storage.Key, ifMatch string) (storage.Row, error) {
	if ifMatch == "" {
		return storage.Row{}, fmt.Errorf("%w: cp requires an ETag precondition", storage.ErrInvalid)
	}
	pk := t.PrimaryKey()
	if len(dest) != len(pk) {
		return storage.Row{}, fmt.Errorf("%w: destination key has %d values, table %q needs %d", storage.ErrNotFound, len(dest), t.Name, len(pk))
	}
	src, err := r.Get(ctx, t.Name, source)
	if err != nil {
		return storage.Row{}, err
	}
	if src.ETag != ifMatch {
		return storage.Row{}, fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
	}
	if err := r.requireAbsentDestination(ctx, t, dest); err != nil {
		return storage.Row{}, err
	}

	destByColumn := make(map[string]storage.Value, len(pk))
	for i, column := range pk {
		destByColumn[column.Name] = dest[i]
	}
	fields := make(map[string]storage.Value, len(t.Columns))
	for i, column := range t.Columns {
		switch {
		case column.Generated:
			continue
		case column.PKOrder > 0:
			fields[column.Name] = destByColumn[column.Name]
		default:
			fields[column.Name] = src.Fields[i].Value
		}
	}
	return r.insert(ctx, t, fields)
}

// rename is `mv` (FR-09): UPDATE of a simple, non-generated primary key. Any
// incoming reference could be affected by the engine, so it is denied
// conservatively instead of being rewritten.
func (r *reader) rename(ctx context.Context, t *catalog.Table, source, dest storage.Key, ifMatch string) (storage.Row, error) {
	if ifMatch == "" {
		return storage.Row{}, fmt.Errorf("%w: mv requires an ETag precondition", storage.ErrInvalid)
	}
	pk := t.PrimaryKey()
	if len(pk) != 1 {
		return storage.Row{}, fmt.Errorf("%w: table %q has a composite primary key, so mv is denied", storage.ErrUnsupported, t.Name)
	}
	if len(t.Incoming) > 0 {
		return storage.Row{}, fmt.Errorf("%w: table %q has incoming references, so mv is denied", storage.ErrUnsupported, t.Name)
	}
	if len(dest) != 1 {
		return storage.Row{}, fmt.Errorf("%w: destination key has %d values, table %q needs 1", storage.ErrNotFound, len(dest), t.Name)
	}
	current, err := r.Get(ctx, t.Name, source)
	if err != nil {
		return storage.Row{}, err
	}
	if current.ETag != ifMatch {
		return storage.Row{}, fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
	}
	if err := r.requireAbsentDestination(ctx, t, dest); err != nil {
		return storage.Row{}, err
	}

	destArg, err := bindValue(dest[0])
	if err != nil {
		return storage.Row{}, err
	}
	keyArgs, err := bindKey(source)
	if err != nil {
		return storage.Row{}, err
	}
	args := append([]any{destArg}, keyArgs...)
	query := "UPDATE " + quoteIdent(t.Name) + " SET " + quoteIdent(pk[0].Name) + " = ? WHERE " + keyConditions(pk)
	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return storage.Row{}, classify(err)
	}
	if err := changedExactlyOne(res); err != nil {
		return storage.Row{}, err
	}
	return r.Get(ctx, t.Name, dest)
}

// remove is `rm` (FR-08): DELETE of exactly one row, never recursive. Incoming
// references are denied even with RESTRICT: their outcome is not part of the
// plan and the MVP stays conservative.
func (r *reader) remove(ctx context.Context, t *catalog.Table, key storage.Key, ifMatch string) (storage.Row, error) {
	if ifMatch == "" {
		return storage.Row{}, fmt.Errorf("%w: rm requires an ETag precondition", storage.ErrInvalid)
	}
	if len(t.Incoming) > 0 {
		return storage.Row{}, fmt.Errorf("%w: table %q has incoming references, so rm is denied", storage.ErrUnsupported, t.Name)
	}
	current, err := r.Get(ctx, t.Name, key)
	if err != nil {
		return storage.Row{}, err
	}
	if current.ETag != ifMatch {
		return storage.Row{}, fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
	}
	args, err := bindKey(key)
	if err != nil {
		return storage.Row{}, err
	}
	query := "DELETE FROM " + quoteIdent(t.Name) + " WHERE " + keyConditions(t.PrimaryKey())
	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return storage.Row{}, classify(err)
	}
	if err := changedExactlyOne(res); err != nil {
		return storage.Row{}, err
	}
	return current, nil
}

// denyCascades is the conservative DESIGN §8 rejection: an engine-side FK
// action can rewrite or null rows the plan never authorized. Outgoing actions
// always deny; incoming ones only matter for write, because a touch cannot fire
// them in SQLite.
func (r *reader) denyCascades(t *catalog.Table, kind string) error {
	for _, fk := range t.ForeignKeys {
		if isCascadeAction(fk.OnUpdate) || isCascadeAction(fk.OnDelete) {
			return fmt.Errorf("%w: table %q has a foreign key with %s/%s actions, so writes are denied",
				storage.ErrUnsupported, t.Name, fk.OnUpdate, fk.OnDelete)
		}
	}
	if kind != "write" {
		return nil
	}
	for _, other := range r.store.snapshot() {
		for _, fk := range other.ForeignKeys {
			// SQLite identifiers are case-insensitive, so the match must be
			// too: a mixed-case REFERENCES clause is still an incoming FK.
			if !strings.EqualFold(fk.Table, t.Name) || !isCascadeAction(fk.OnUpdate) && !isCascadeAction(fk.OnDelete) {
				continue
			}
			return fmt.Errorf("%w: table %q is referenced by %q with %s/%s actions, so writes are denied",
				storage.ErrUnsupported, t.Name, other.Name, fk.OnUpdate, fk.OnDelete)
		}
	}
	return nil
}

func isCascadeAction(action string) bool {
	switch action {
	case "CASCADE", "SET NULL", "SET DEFAULT":
		return true
	}
	return false
}

// insert is `touch`: an INSERT with only the columns the caller supplied, so
// omitted columns keep their native defaults and NOT NULL constraints still
// speak for themselves. It returns the effective row with its ETag.
func (r *reader) insert(ctx context.Context, t *catalog.Table, fields map[string]storage.Value) (storage.Row, error) {
	var names []string
	if len(fields) > 0 {
		var err error
		names, err = writeNames(t, fields, true)
		if err != nil {
			return storage.Row{}, err
		}
	}

	var query string
	var args []any
	if len(names) == 0 {
		query = "INSERT INTO " + quoteIdent(t.Name) + " DEFAULT VALUES"
	} else {
		placeholders := make([]string, len(names))
		columns := make([]string, len(names))
		args = make([]any, len(names))
		for i, name := range names {
			columns[i] = quoteIdent(name)
			placeholders[i] = "?"
			arg, err := bindValue(fields[name])
			if err != nil {
				return storage.Row{}, err
			}
			args[i] = arg
		}
		query = "INSERT INTO " + quoteIdent(t.Name) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	}

	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return storage.Row{}, classify(err)
	}
	if err := changedExactlyOne(res); err != nil {
		return storage.Row{}, err
	}
	key, err := effectiveKey(t, fields, res)
	if err != nil {
		return storage.Row{}, err
	}
	return r.Get(ctx, t.Name, key)
}

// upsert is REF-04: one INSERT ... ON CONFLICT(<primary key>) DO UPDATE inside
// the caller's IMMEDIATE transaction. The destination is the primary key; a
// conflict on any other UNIQUE aborts the statement, which classify turns into
// E_CONSTRAINT, so the upsert never updates a row the caller did not identify.
// The ETag precondition is mandatory for an existing row and compared here, in
// the same transaction, before any SQL runs.
func (r *reader) upsert(ctx context.Context, t *catalog.Table, key storage.Key, fields map[string]storage.Value, ifMatch string) (storage.Row, error) {
	current, getErr := r.Get(ctx, t.Name, key)
	exists := getErr == nil
	if getErr != nil && !errors.Is(getErr, storage.ErrNotFound) {
		return storage.Row{}, getErr
	}
	kind := "touch"
	if exists {
		kind = "write"
		if ifMatch == "" {
			return storage.Row{}, fmt.Errorf("%w: upsert of an existing row requires an ETag precondition", storage.ErrInvalid)
		}
		if current.ETag != ifMatch {
			return storage.Row{}, fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
		}
	} else if ifMatch != "" {
		return storage.Row{}, fmt.Errorf("%w: ETag precondition given for a row that does not exist in table %q", storage.ErrInvalid, t.Name)
	}
	if err := r.denyCascades(t, kind); err != nil {
		return storage.Row{}, err
	}

	names, err := writeNames(t, fields, true)
	if err != nil {
		return storage.Row{}, err
	}
	columns := make([]string, len(names))
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		columns[i] = quoteIdent(name)
		placeholders[i] = "?"
		arg, err := bindValue(fields[name])
		if err != nil {
			return storage.Row{}, err
		}
		args[i] = arg
	}
	pk := t.PrimaryKey()
	conflict := make([]string, len(pk))
	for i, column := range pk {
		conflict[i] = quoteIdent(column.Name)
	}
	query := "INSERT INTO " + quoteIdent(t.Name) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")" +
		" ON CONFLICT (" + strings.Join(conflict, ", ") + ")"

	var sets []string
	for _, name := range names {
		column, ok := findColumn(t, name)
		if !ok || column.PKOrder > 0 {
			continue
		}
		sets = append(sets, quoteIdent(name)+" = ?")
		arg, err := bindValue(fields[name])
		if err != nil {
			return storage.Row{}, err
		}
		args = append(args, arg)
	}
	if len(sets) == 0 {
		query += " DO NOTHING"
	} else {
		query += " DO UPDATE SET " + strings.Join(sets, ", ")
	}

	if _, err := r.q.ExecContext(ctx, query, args...); err != nil {
		return storage.Row{}, classify(err)
	}
	return r.Get(ctx, t.Name, key)
}

// update is `write`: a partial UPDATE of authorized fields, never the key and
// never a generated column. The ETag precondition is mandatory and is compared
// inside the transaction before any SQL runs.
func (r *reader) update(ctx context.Context, t *catalog.Table, key storage.Key, fields map[string]storage.Value, ifMatch string) (storage.Row, error) {
	if ifMatch == "" {
		return storage.Row{}, fmt.Errorf("%w: write requires an ETag precondition", storage.ErrInvalid)
	}
	current, err := r.Get(ctx, t.Name, key)
	if err != nil {
		return storage.Row{}, err
	}
	if current.ETag != ifMatch {
		return storage.Row{}, fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
	}

	names, err := writeNames(t, fields, false)
	if err != nil {
		return storage.Row{}, err
	}
	sets := make([]string, len(names))
	args := make([]any, 0, len(names)+len(key))
	for i, name := range names {
		sets[i] = quoteIdent(name) + " = ?"
		arg, err := bindValue(fields[name])
		if err != nil {
			return storage.Row{}, err
		}
		args = append(args, arg)
	}
	keyArgs, err := bindKey(key)
	if err != nil {
		return storage.Row{}, err
	}
	args = append(args, keyArgs...)

	query := "UPDATE " + quoteIdent(t.Name) + " SET " + strings.Join(sets, ", ") +
		" WHERE " + keyConditions(t.PrimaryKey())
	res, err := r.q.ExecContext(ctx, query, args...)
	if err != nil {
		return storage.Row{}, classify(err)
	}
	if err := changedExactlyOne(res); err != nil {
		return storage.Row{}, err
	}
	return r.Get(ctx, t.Name, key)
}

// writeNames validates the field set against the catalog and returns it in
// byte-wise order so the SQL is deterministic. Generated columns are never
// writable; primary-key columns are writable only on insert.
func writeNames(t *catalog.Table, fields map[string]storage.Value, allowKey bool) ([]string, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: mutation has no fields", storage.ErrInvalid)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		column, ok := findColumn(t, name)
		if !ok {
			return nil, fmt.Errorf("%w: unknown column %q", storage.ErrInvalid, name)
		}
		if column.Generated {
			return nil, fmt.Errorf("%w: column %q is generated", storage.ErrUnsupported, name)
		}
		if column.PKOrder > 0 && !allowKey {
			return nil, fmt.Errorf("%w: column %q is part of the primary key", storage.ErrUnsupported, name)
		}
	}
	return names, nil
}

func findColumn(t *catalog.Table, name string) (storage.Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return storage.Column{}, false
}

func changedExactlyOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("%w: mutation affected %d rows, want 1", storage.ErrConflict, n)
	}
	return nil
}

// effectiveKey determines the identity of the row just inserted. A supplied
// primary key is used as is; an omitted single INTEGER PRIMARY KEY (the rowid
// alias) comes from LastInsertId. Any other omission cannot be addressed by
// path, so it is denied rather than guessed.
func effectiveKey(t *catalog.Table, fields map[string]storage.Value, res sql.Result) (storage.Key, error) {
	pk := t.PrimaryKey()
	key := make(storage.Key, 0, len(pk))
	for _, column := range pk {
		v, ok := fields[column.Name]
		if !ok {
			if len(pk) == 1 && catalog.Affinity(pk[0].DeclaredType) == "INTEGER" {
				id, err := res.LastInsertId()
				if err != nil {
					return nil, fmt.Errorf("read inserted key: %w", err)
				}
				return storage.Key{{Kind: "integer", Data: strconv.FormatInt(id, 10)}}, nil
			}
			return nil, fmt.Errorf("%w: cannot determine the effective key of table %q without its primary key", storage.ErrUnsupported, t.Name)
		}
		if v.Kind != "text" && v.Kind != "integer" {
			return nil, fmt.Errorf("%w: primary key kind %q is not navigable", storage.ErrUnsupported, v.Kind)
		}
		key = append(key, v)
	}
	return key, nil
}
