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

// Copy inserts a new row in the same table with the source's ordinary columns
// and an explicit destination key (FR-07).
func (s Service) Copy(ctx context.Context, source, dest vfs.Path, ifMatch string, opts MutationOptions) (any, error) {
	return s.relocate(ctx, "cp", source, dest, ifMatch, opts)
}

// Move renames the simple primary key of one row within its table (FR-09).
func (s Service) Move(ctx context.Context, source, dest vfs.Path, ifMatch string, opts MutationOptions) (any, error) {
	return s.relocate(ctx, "mv", source, dest, ifMatch, opts)
}

// Remove deletes exactly one row (FR-08).
func (s Service) Remove(ctx context.Context, p vfs.Path, ifMatch string, opts MutationOptions) (any, error) {
	if p.Kind != vfs.KindRow {
		err := fmt.Errorf("%w: rm needs a row path", storage.ErrUnsupported)
		s.auditFailure("rm", p, state.Mount{}, opts, err)
		return nil, err
	}
	m, err := s.mount(p.Alias)
	if err != nil {
		return nil, err
	}
	rowPath, err := pathString(p)
	if err != nil {
		return nil, err
	}
	if err := mutationAllowed(m, "rm", rowPath); err != nil {
		_ = s.auditOutcome("rm", p, m, opts, "denied", "E_DENIED")
		return nil, err
	}
	// The ETag is computed from the whole row, so reading it requires the same
	// full-row access as cat (DESIGN §8).
	if !readOpAllowed(m, "cat", rowPath) {
		_ = s.auditOutcome("rm", p, m, opts, "denied", "E_DENIED")
		return nil, policyDenied("rm", "row", rowPath)
	}
	if ifMatch == "" {
		err := fmt.Errorf("%w: rm requires an ETag precondition", storage.ErrInvalid)
		s.auditFailure("rm", p, m, opts, err)
		return nil, err
	}

	st, err := s.open(p.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	if opts.DryRun {
		if err := planRemove(ctx, st, p, ifMatch); err != nil {
			s.auditFailure("rm", p, m, opts, err)
			return nil, err
		}
		_ = s.auditOutcome("rm", p, m, opts, "dry-run", "OK")
		return MutationPlan{Op: "rm", Path: rowPath, Table: p.Table, IfMatch: ifMatch, DryRun: true, Warning: dryRunWarning}, nil
	}

	t, err := st.Table(ctx, p.Table)
	if err != nil {
		s.auditFailure("rm", p, m, opts, err)
		return nil, err
	}
	if err := checkRemoveTarget(t); err != nil {
		s.auditFailure("rm", p, m, opts, err)
		return nil, err
	}
	key, err := resolveKey(p.Key, t.Table)
	if err != nil {
		s.auditFailure("rm", p, m, opts, err)
		return nil, err
	}
	return s.execute(ctx, st, "rm", p, nil, m, opts, storage.Mutation{Kind: "rm", Table: p.Table, Key: key, IfMatch: ifMatch})
}

// relocate is the shared cp/mv path: authorize, plan or execute. Both stay
// inside one table, so the source and destination rows must name the same alias
// and table.
func (s Service) relocate(ctx context.Context, op string, source, dest vfs.Path, ifMatch string, opts MutationOptions) (any, error) {
	if source.Kind != vfs.KindRow || dest.Kind != vfs.KindRow {
		err := fmt.Errorf("%w: %s needs a source row and a destination row", storage.ErrUnsupported, op)
		s.auditFailure(op, source, state.Mount{}, opts, err)
		return nil, err
	}
	if source.Alias != dest.Alias || source.Table != dest.Table {
		err := fmt.Errorf("%w: %s stays within one table", storage.ErrUnsupported, op)
		s.auditFailure(op, source, state.Mount{}, opts, err)
		return nil, err
	}
	m, err := s.mount(source.Alias)
	if err != nil {
		return nil, err
	}
	srcPath, err := pathString(source)
	if err != nil {
		return nil, err
	}
	dstPath, err := pathString(dest)
	if err != nil {
		return nil, err
	}
	grants := []string{dstPath}
	if op == "mv" {
		// Renaming changes the identity of the source row, so its own grant
		// must cover the operation too.
		grants = append(grants, srcPath)
	}
	for _, target := range grants {
		if err := mutationAllowed(m, op, target); err != nil {
			_ = s.auditOutcome(op, source, m, opts, "denied", "E_DENIED")
			return nil, err
		}
	}
	if !readOpAllowed(m, "cat", srcPath) {
		_ = s.auditOutcome(op, source, m, opts, "denied", "E_DENIED")
		return nil, policyDenied(op, "row", srcPath)
	}
	if ifMatch == "" {
		err := fmt.Errorf("%w: %s requires an ETag precondition", storage.ErrInvalid, op)
		s.auditFailure(op, source, m, opts, err)
		return nil, err
	}

	st, err := s.open(source.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	if opts.DryRun {
		if err := planRelocate(ctx, st, op, source, dest, ifMatch); err != nil {
			s.auditFailure(op, source, m, opts, err)
			return nil, err
		}
		_ = s.auditOutcome(op, source, m, opts, "dry-run", "OK")
		return MutationPlan{
			Op: op, Path: srcPath, Table: source.Table, Destination: dstPath,
			IfMatch: ifMatch, DryRun: true, Warning: dryRunWarning,
		}, nil
	}

	t, err := st.Table(ctx, source.Table)
	if err != nil {
		s.auditFailure(op, source, m, opts, err)
		return nil, err
	}
	if err := checkRelocationTarget(op, t); err != nil {
		s.auditFailure(op, source, m, opts, err)
		return nil, err
	}
	src, dst, err := resolveRelocationKeys(t, source, dest)
	if err != nil {
		s.auditFailure(op, source, m, opts, err)
		return nil, err
	}
	return s.execute(ctx, st, op, source, &dest, m, opts, storage.Mutation{
		Kind: op, Table: source.Table, Key: src, Destination: dst, IfMatch: ifMatch,
	})
}

// checkRelocationTarget is the structural half of cp/mv: the table must be
// writable and, for mv, address by a simple primary key with no incoming
// references. The adapter revalidates it inside the transaction.
func checkRelocationTarget(op string, t catalog.Table) error {
	if err := writableTable(t); err != nil {
		return err
	}
	if op != "mv" {
		return nil
	}
	if len(t.PrimaryKey()) != 1 {
		return fmt.Errorf("%w: table %q has a composite primary key, so mv is denied", storage.ErrUnsupported, t.Name)
	}
	if len(t.Incoming) > 0 {
		return fmt.Errorf("%w: table %q has incoming references, so mv is denied", storage.ErrUnsupported, t.Name)
	}
	return nil
}

// checkRemoveTarget rejects rm on a table whose rows another table references.
func checkRemoveTarget(t catalog.Table) error {
	if err := writableTable(t); err != nil {
		return err
	}
	if len(t.Incoming) > 0 {
		return fmt.Errorf("%w: table %q has incoming references, so rm is denied", storage.ErrUnsupported, t.Name)
	}
	return nil
}

// resolveRelocationKeys types both path keys against the catalog before the
// write transaction starts; the adapter validates them again inside it.
func resolveRelocationKeys(t catalog.Table, source, dest vfs.Path) (storage.Key, storage.Key, error) {
	src, err := resolveKey(source.Key, t.Table)
	if err != nil {
		return nil, nil, err
	}
	dst, err := resolveKey(dest.Key, t.Table)
	if err != nil {
		return nil, nil, err
	}
	return src, dst, nil
}

// planRelocate consults the source ETag, the destination's absence, the simple
// key and the incoming references a mv would reach, without writing.
func planRelocate(ctx context.Context, st *sqlite.Store, op string, source, dest vfs.Path, ifMatch string) error {
	t, err := st.Table(ctx, source.Table)
	if err != nil {
		return err
	}
	if err := checkRelocationTarget(op, t); err != nil {
		return err
	}
	src, dst, err := resolveRelocationKeys(t, source, dest)
	if err != nil {
		return err
	}
	return st.Read(ctx, func(rd storage.Reader) error {
		row, err := rd.Get(ctx, t.Name, src)
		if err != nil {
			return err
		}
		if row.ETag != ifMatch {
			return fmt.Errorf("%w: ETag %q no longer matches table %q", storage.ErrConflict, ifMatch, t.Name)
		}
		_, err = rd.Get(ctx, t.Name, dst)
		switch {
		case err == nil:
			return fmt.Errorf("%w: destination row already exists in table %q", storage.ErrConflict, t.Name)
		case errors.Is(err, storage.ErrNotFound):
			return nil
		default:
			return err
		}
	})
}

// planRemove consults the ETag and the incoming references a rm would reach.
func planRemove(ctx context.Context, st *sqlite.Store, p vfs.Path, ifMatch string) error {
	t, err := st.Table(ctx, p.Table)
	if err != nil {
		return err
	}
	if err := checkRemoveTarget(t); err != nil {
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
