package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/storage/sqlite"
	"dbvfs/internal/vfs"
)

// batchLimit is D-8: 100 operations is both the default and the maximum.
const batchLimit = 100

// batchItem is one element of the --data array, before path decoding. The
// strict decoder rejects unknown keys so a typo is E_USAGE, not a silent no-op.
type batchItem struct {
	Op      string          `json:"op"`
	Path    string          `json:"path"`
	Dest    string          `json:"dest"`
	Data    json.RawMessage `json:"data"`
	IfMatch string          `json:"if_match"`
}

// batchSpec is one parsed operation: its decoded paths and its own payload,
// whose fields are parsed exactly as the individual commands parse theirs.
type batchSpec struct {
	op      string
	path    vfs.Path
	dest    *vfs.Path
	data    []byte
	ifMatch string
}

// batchOperation is one authorized operation ready to apply, with its rendered
// paths for plans and the single storage mutation it contributes.
type batchOperation struct {
	index    int
	op       string
	path     vfs.Path
	dest     *vfs.Path
	srcPath  string
	destPath string
	fields   map[string]storage.Value
	ifMatch  string
	mutation storage.Mutation
}

// batchPlanItem and batchResultItem keep the response ordered by input index.
type batchPlanItem struct {
	Index int          `json:"index"`
	Op    string       `json:"op"`
	Plan  MutationPlan `json:"plan"`
}

type batchResultItem struct {
	Index int    `json:"index"`
	Op    string `json:"op"`
	RowData
}

// batchData is the success shape: a deterministic list, never null.
type batchData struct {
	Operations []any `json:"operations"`
}

// Batch applies an explicit array of mutations on one mount in one
// transaction (FR-12, DESIGN §8). A failure reverts every operation; the
// response lists one result per operation in input order. The explicit array is
// the confirmation an individual mv or rm would take from --apply.
func (s Service) Batch(ctx context.Context, data []byte, opts MutationOptions) (any, error) {
	specs, err := parseBatch(data)
	if err != nil {
		return nil, err
	}
	alias := specs[0].path.Alias
	for i, spec := range specs {
		var mismatch error
		switch {
		case spec.path.Alias != alias:
			mismatch = fmt.Errorf("%w: batch operation %d targets alias %q; one batch targets one mount (%q)", storage.ErrInvalid, i, spec.path.Alias, alias)
		case spec.dest != nil && spec.dest.Alias != alias:
			mismatch = fmt.Errorf("%w: batch operation %d targets alias %q; one batch targets one mount (%q)", storage.ErrInvalid, i, spec.dest.Alias, alias)
		default:
			continue
		}
		// The rejection happens before the store opens, but the operator should
		// still see the attempt: audit it against the first alias when it
		// resolves, and skip the record when the mount itself is unknown.
		if m, mountErr := s.mount(alias); mountErr == nil {
			s.auditFailure(spec.op, spec.path, m, opts, mismatch)
		}
		return nil, mismatch
	}
	m, err := s.mount(alias)
	if err != nil {
		return nil, err
	}

	st, err := s.open(alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	ops := make([]batchOperation, 0, len(specs))
	for i, spec := range specs {
		op, err := prepareBatchOp(ctx, st, m, i, spec)
		if err != nil {
			return nil, s.batchFailure(i, spec.op, spec.path, m, opts, err)
		}
		ops = append(ops, op)
	}
	if opts.DryRun {
		return s.planBatch(ctx, st, m, opts, ops)
	}
	return s.executeBatch(ctx, st, m, opts, ops)
}

// parseBatch decodes the array and validates each wrapper. Paths are decoded
// here, data stays raw so parseMutationFields can reject its own edge cases.
func parseBatch(data []byte) ([]batchSpec, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: batch --data must be a JSON array of operations", storage.ErrInvalid)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: batch needs at least one operation", storage.ErrInvalid)
	}
	if len(raw) > batchLimit {
		return nil, fmt.Errorf("%w: batch has %d operations; the maximum is %d", storage.ErrLimit, len(raw), batchLimit)
	}

	specs := make([]batchSpec, 0, len(raw))
	for i, element := range raw {
		dec := json.NewDecoder(bytes.NewReader(element))
		dec.DisallowUnknownFields()
		var item batchItem
		if err := dec.Decode(&item); err != nil {
			return nil, fmt.Errorf("%w: batch operation %d: %v", storage.ErrInvalid, i, err)
		}
		spec, err := parseBatchItem(item)
		if err != nil {
			return nil, fmt.Errorf("batch operation %d: %w", i, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// parseBatchItem turns one wrapper into a typed operation. Each key belongs to
// exactly one kind of operation, so caches of a wrong key are usage errors.
func parseBatchItem(item batchItem) (batchSpec, error) {
	spec := batchSpec{op: item.Op, data: []byte(item.Data), ifMatch: item.IfMatch}
	switch item.Op {
	case "touch", "write", "cp", "mv", "rm":
	default:
		return spec, fmt.Errorf("%w: unknown batch operation %q", storage.ErrInvalid, item.Op)
	}
	if item.Path == "" {
		return spec, fmt.Errorf("%w: %s needs a path", storage.ErrInvalid, item.Op)
	}
	path, err := vfs.Parse(item.Path)
	if err != nil {
		return spec, err
	}
	spec.path = path

	relocate := item.Op == "cp" || item.Op == "mv"
	switch {
	case relocate && item.Dest == "":
		return spec, fmt.Errorf("%w: %s needs a dest row", storage.ErrInvalid, item.Op)
	case !relocate && item.Dest != "":
		return spec, fmt.Errorf("%w: %s does not take dest", storage.ErrInvalid, item.Op)
	}
	if item.Dest != "" {
		dest, err := vfs.Parse(item.Dest)
		if err != nil {
			return spec, err
		}
		spec.dest = &dest
	}

	mutates := item.Op == "touch" || item.Op == "write"
	switch {
	case mutates && item.Data == nil:
		return spec, fmt.Errorf("%w: %s needs data", storage.ErrInvalid, item.Op)
	case !mutates && item.Data != nil:
		return spec, fmt.Errorf("%w: %s does not take data", storage.ErrInvalid, item.Op)
	}

	if item.Op != "touch" && item.IfMatch == "" {
		return spec, fmt.Errorf("%w: %s requires an if_match ETag precondition", storage.ErrInvalid, item.Op)
	}
	if item.Op == "touch" && item.IfMatch != "" {
		return spec, fmt.Errorf("%w: touch does not take if_match", storage.ErrInvalid)
	}
	return spec, nil
}

// prepareBatchOp mirrors the authorization and structural preconditions of the
// individual mutation commands, reusing their parsers and key resolvers.
func prepareBatchOp(ctx context.Context, st *sqlite.Store, m state.Mount, index int, spec batchSpec) (batchOperation, error) {
	op := batchOperation{index: index, op: spec.op, path: spec.path, dest: spec.dest, ifMatch: spec.ifMatch}
	var err error
	if op.srcPath, err = pathString(spec.path); err != nil {
		return op, err
	}
	if spec.dest != nil {
		if op.destPath, err = pathString(*spec.dest); err != nil {
			return op, err
		}
	}

	switch spec.op {
	case "touch":
		if spec.path.Kind != vfs.KindTable {
			return op, fmt.Errorf("%w: touch needs a table path /ALIAS/TABLE", storage.ErrUnsupported)
		}
		if err := mutationAllowed(m, "touch", op.srcPath); err != nil {
			return op, err
		}
		if !readOpAllowed(m, "cat", op.srcPath) {
			return op, policyDenied("touch", "row", op.srcPath)
		}
		fields, err := parseMutationFields(spec.path, spec.data)
		if err != nil {
			return op, err
		}
		op.fields = fields
		op.mutation = storage.Mutation{Kind: "touch", Table: spec.path.Table, Fields: fields}

	case "write":
		if spec.path.Kind != vfs.KindRow && spec.path.Kind != vfs.KindField {
			return op, fmt.Errorf("%w: write needs a row or field path", storage.ErrUnsupported)
		}
		fields, err := parseMutationFields(spec.path, spec.data)
		if err != nil {
			return op, err
		}
		if err := authorizeFieldWrites(m, spec.path, fields); err != nil {
			return op, err
		}
		rowPath, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: spec.path.Alias, Table: spec.path.Table, Key: spec.path.Key})
		if err != nil {
			return op, err
		}
		if !readOpAllowed(m, "cat", rowPath) {
			return op, policyDenied("write", "row", rowPath)
		}
		key, err := resolveKeyAgainstStore(ctx, st, spec.path)
		if err != nil {
			return op, err
		}
		op.fields = fields
		op.mutation = storage.Mutation{Kind: "write", Table: spec.path.Table, Key: key, Fields: fields, IfMatch: spec.ifMatch}

	case "cp", "mv":
		if spec.path.Kind != vfs.KindRow || spec.dest.Kind != vfs.KindRow {
			return op, fmt.Errorf("%w: %s needs a source row and a destination row", storage.ErrUnsupported, spec.op)
		}
		if spec.path.Alias != spec.dest.Alias || spec.path.Table != spec.dest.Table {
			return op, fmt.Errorf("%w: %s stays within one table", storage.ErrUnsupported, spec.op)
		}
		grants := []string{op.destPath}
		if spec.op == "mv" {
			grants = append(grants, op.srcPath)
		}
		for _, target := range grants {
			if err := mutationAllowed(m, spec.op, target); err != nil {
				return op, err
			}
		}
		if !readOpAllowed(m, "cat", op.srcPath) {
			return op, policyDenied(spec.op, "row", op.srcPath)
		}
		t, err := st.Table(ctx, spec.path.Table)
		if err != nil {
			return op, err
		}
		if err := checkRelocationTarget(spec.op, t); err != nil {
			return op, err
		}
		src, dst, err := resolveRelocationKeys(t, spec.path, *spec.dest)
		if err != nil {
			return op, err
		}
		op.mutation = storage.Mutation{Kind: spec.op, Table: spec.path.Table, Key: src, Destination: dst, IfMatch: spec.ifMatch}

	case "rm":
		if spec.path.Kind != vfs.KindRow {
			return op, fmt.Errorf("%w: rm needs a row path", storage.ErrUnsupported)
		}
		if err := mutationAllowed(m, "rm", op.srcPath); err != nil {
			return op, err
		}
		if !readOpAllowed(m, "cat", op.srcPath) {
			return op, policyDenied("rm", "row", op.srcPath)
		}
		t, err := st.Table(ctx, spec.path.Table)
		if err != nil {
			return op, err
		}
		if err := checkRemoveTarget(t); err != nil {
			return op, err
		}
		key, err := resolveKey(spec.path.Key, t.Table)
		if err != nil {
			return op, err
		}
		op.mutation = storage.Mutation{Kind: "rm", Table: spec.path.Table, Key: key, IfMatch: spec.ifMatch}
	}
	return op, nil
}

// planBatch plans every operation without writing, reusing the same planners
// the individual --dry-run commands use. Constraints and triggers stay
// unproven, which the shared warning states.
func (s Service) planBatch(ctx context.Context, st *sqlite.Store, m state.Mount, opts MutationOptions, ops []batchOperation) (any, error) {
	items := make([]any, 0, len(ops))
	for _, op := range ops {
		var err error
		switch op.op {
		case "touch":
			err = planTouch(ctx, st, op.path.Table, op.fields)
		case "write":
			err = planWrite(ctx, st, op.path, op.fields, op.ifMatch)
		case "cp", "mv":
			err = planRelocate(ctx, st, op.op, op.path, *op.dest, op.ifMatch)
		case "rm":
			err = planRemove(ctx, st, op.path, op.ifMatch)
		}
		if err != nil {
			return nil, s.batchFailure(op.index, op.op, op.path, m, opts, err)
		}
		plan := MutationPlan{
			Op: op.op, Path: op.srcPath, Table: op.path.Table, Destination: op.destPath,
			Fields: op.fields, IfMatch: op.ifMatch, DryRun: true, Warning: dryRunWarning,
		}
		_ = s.auditRecord(op.op, op.path, op.dest, m, opts, "dry-run", "OK")
		items = append(items, batchPlanItem{Index: op.index, Op: op.op, Plan: plan})
	}
	return batchData{Operations: items}, nil
}

// executeBatch applies every operation inside a single store.Write, so the
// batch shares one BEGIN IMMEDIATE transaction. Attempts are audited before the
// transaction opens because the audit log is not part of it: recording inside
// would only hold the write lock longer. Results are audited only after the
// transaction commits, because a rollback leaves none of them in effect.
func (s Service) executeBatch(ctx context.Context, st *sqlite.Store, m state.Mount, opts MutationOptions, ops []batchOperation) (any, error) {
	rows := make([]storage.Row, len(ops))
	for _, op := range ops {
		if err := s.auditRecord(op.op, op.path, op.dest, m, opts, "attempt", ""); err != nil {
			return nil, fmt.Errorf("record audit attempt for %s: %w", op.op, err)
		}
	}

	failed := -1
	var failure error
	writeErr := st.Write(ctx, func(tx storage.Tx) error {
		for i, op := range ops {
			row, err := tx.Apply(ctx, op.mutation)
			if err != nil {
				failed, failure = i, err
				return batchError(op.index, op.op, err)
			}
			rows[i] = row
		}
		return nil
	})
	if writeErr != nil {
		if errors.Is(writeErr, storage.ErrCommitUnknown) {
			// The batch may have committed as a whole: every operation is
			// uncertain, never partially confirmed.
			for _, op := range ops {
				_ = s.auditRecord(op.op, op.path, op.dest, m, opts, "unknown", "E_COMMIT_UNKNOWN")
			}
			return nil, writeErr
		}
		// A failure with no failed index never reached the callback (busy on
		// acquire or on BEGIN), so every attempted operation is an error.
		code := auditCode(writeErr)
		if failure != nil {
			code = auditCode(failure)
		}
		audited := ops
		if failed >= 0 {
			audited = ops[:failed+1]
		}
		for _, op := range audited {
			_ = s.auditRecord(op.op, op.path, op.dest, m, opts, "error", code)
		}
		return nil, writeErr
	}

	items := make([]any, 0, len(ops))
	for i, op := range ops {
		if err := s.auditRecord(op.op, op.path, op.dest, m, opts, "ok", "OK"); err != nil {
			return nil, fmt.Errorf("%w: batch committed but its final audit record failed: %v; read the database instead of retrying", storage.ErrCommitUnknown, err)
		}
		path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: op.path.Alias, Table: op.path.Table, Key: rows[i].Key})
		if err != nil {
			return nil, err
		}
		items = append(items, batchResultItem{
			Index: op.index,
			Op:    op.op,
			RowData: RowData{
				Path:   path,
				Fields: fieldMap(rows[i]),
				ETag:   rows[i].ETag,
			},
		})
	}
	return batchData{Operations: items}, nil
}

// batchFailure audits one rejected operation and wraps its error with the
// deterministic index and operation the client needs (NFR-01).
func (s Service) batchFailure(index int, op string, path vfs.Path, m state.Mount, opts MutationOptions, err error) error {
	status, code := "error", auditCode(err)
	if errors.Is(err, storage.ErrDenied) {
		status, code = "denied", "E_DENIED"
	}
	_ = s.auditRecord(op, path, nil, m, opts, status, code)
	return batchError(index, op, err)
}

func batchError(index int, op string, err error) error {
	return fmt.Errorf("batch operation %d (%s): %w", index, op, err)
}
