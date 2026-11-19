package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"dbvfs/internal/storage"
)

// comparisonOps is ordered longest first so "!=" and "<=" win over "=" and
// "<" at the same position (DESIGN §7).
var comparisonOps = []string{"!=", "<=", ">=", "=", "<", ">"}

// parseWhere turns the repeatable --where expressions into typed filters. Every
// identifier is checked against the catalog and every value stays a bound
// parameter, so no expression ever becomes SQL (NFR-02).
func parseWhere(exprs []string, t storage.Table) ([]storage.Filter, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	filters := make([]storage.Filter, 0, len(exprs))
	for _, expr := range exprs {
		f, err := parseFilter(expr, t)
		if err != nil {
			return nil, err
		}
		filters = append(filters, f)
	}
	return filters, nil
}

func parseFilter(expr string, t storage.Table) (storage.Filter, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{"is-null", "not-null"} {
		for _, sep := range []string{" ", "="} {
			if strings.HasSuffix(expr, sep+op) {
				column := strings.TrimSpace(strings.TrimSuffix(expr, sep+op))
				return newFilter(column, op, "", t)
			}
		}
	}

	at, op := -1, ""
	for i := 0; i < len(expr) && at < 0; i++ {
		for _, candidate := range comparisonOps {
			if strings.HasPrefix(expr[i:], candidate) {
				at, op = i, candidate
				break
			}
		}
	}
	if at < 0 {
		return storage.Filter{}, fmt.Errorf("%w: --where %q has no comparison operator", storage.ErrInvalid, expr)
	}
	column := strings.TrimSpace(expr[:at])
	raw := strings.TrimSpace(expr[at+len(op):])
	return newFilter(column, op, raw, t)
}

func newFilter(column, op, raw string, t storage.Table) (storage.Filter, error) {
	if column == "" {
		return storage.Filter{}, fmt.Errorf("%w: --where has an empty column", storage.ErrInvalid)
	}
	if !t.HasColumn(column) {
		return storage.Filter{}, fmt.Errorf("%w: unknown column %q", storage.ErrInvalid, column)
	}
	if op == "is-null" || op == "not-null" {
		return storage.Filter{Column: column, Op: op}, nil
	}
	value, err := parseFilterValue(raw)
	if err != nil {
		return storage.Filter{}, err
	}
	if value.Kind == "null" {
		switch op {
		case "=":
			return storage.Filter{Column: column, Op: "is-null"}, nil
		case "!=":
			return storage.Filter{Column: column, Op: "not-null"}, nil
		}
		return storage.Filter{}, fmt.Errorf("%w: operator %q cannot compare against null", storage.ErrInvalid, op)
	}
	return storage.Filter{Column: column, Op: op, Value: value}, nil
}

// parseFilterValue reads the value as JSON first and falls back to a literal
// text, as DESIGN §7 requires: 1 is an integer, "1" is text, true/false are
// numeric literals, and a non-JSON word is text.
func parseFilterValue(raw string) (storage.Value, error) {
	if raw == "" {
		return storage.Value{Kind: "text"}, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err == nil {
		var trailing any
		if err := dec.Decode(&trailing); err == io.EOF {
			return jsonValue(v)
		}
	}
	return storage.Value{Kind: "text", Data: raw}, nil
}

func jsonValue(v any) (storage.Value, error) {
	switch x := v.(type) {
	case nil:
		return storage.Value{Kind: "null"}, nil
	case bool:
		if x {
			return storage.Value{Kind: "integer", Data: "1"}, nil
		}
		return storage.Value{Kind: "integer", Data: "0"}, nil
	case string:
		return storage.Value{Kind: "text", Data: x}, nil
	case json.Number:
		raw := string(x)
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return storage.Value{Kind: "integer", Data: strconv.FormatInt(n, 10)}, nil
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return storage.Value{}, fmt.Errorf("%w: --where number %q is not a finite REAL", storage.ErrInvalid, raw)
		}
		return storage.Value{Kind: "real", Data: strconv.FormatFloat(f, 'g', -1, 64)}, nil
	}
	return storage.Value{}, fmt.Errorf("%w: only scalar JSON values are supported", storage.ErrInvalid)
}
