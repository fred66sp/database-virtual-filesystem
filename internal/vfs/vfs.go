// Package vfs implements the virtual path codec from DESIGN §4.
//
// A path is absolute and has at most four segments: /alias/table/key/field.
// Segments are resolved (literal "." and "..") before they are percent-decoded,
// so an encoded dot is data, never traversal. Decoded segments are re-encoded
// and compared with the input, which makes every accepted path canonical:
// "%2f", "%41" or a raw multi-byte character are rejected, not normalized.
package vfs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"dbvfs/internal/storage"
)

// ErrInvalidPath marks every codec rejection. Callers classify it once, with
// errors.Is, at the CLI frontier.
var ErrInvalidPath = errors.New("invalid virtual path")

// Kind is the resource class a path addresses.
type Kind int

const (
	KindRoot Kind = iota
	KindMount
	KindTable
	KindRow
	KindField
)

// Value kinds used by the path codec. DESIGN §4 treats real, blob and null
// identities as non-navigable, so they never form a path segment.
const (
	kindText    = "text"
	kindInteger = "integer"
)

// Path is a parsed virtual path. Fields are meaningful up to Kind:
// Alias (mount and deeper), Table (table and deeper), Key (row and deeper),
// Field (field only).
type Path struct {
	Kind  Kind
	Alias string
	Table string
	Key   storage.Key
	Field string
}

// Parse decodes an absolute virtual path. The returned Path is canonical: its
// Canonical form is the input, modulo resolution of literal "." and "..".
func Parse(p string) (Path, error) {
	if p == "" {
		return Path{}, invalid("path is empty")
	}
	if p[0] != '/' {
		return Path{}, invalid("path must be absolute")
	}
	raw := p[1:]
	if raw == "" {
		return Path{Kind: KindRoot}, nil
	}

	resolved := make([]string, 0, 4)
	for _, seg := range strings.Split(raw, "/") {
		switch seg {
		case "":
			return Path{}, invalid("empty path segment")
		case ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return Path{}, invalid("path escapes root")
			}
			resolved = resolved[:len(resolved)-1]
		default:
			resolved = append(resolved, seg)
		}
	}

	if len(resolved) > 4 {
		return Path{}, invalid("path is deeper than /alias/table/key/field")
	}
	if len(resolved) == 0 {
		return Path{Kind: KindRoot}, nil
	}

	alias, err := decodeAlias(resolved[0])
	if err != nil {
		return Path{}, err
	}
	path := Path{Kind: KindMount, Alias: alias}
	if len(resolved) == 1 {
		return path, nil
	}

	table, err := decodeSegment(resolved[1])
	if err != nil {
		return Path{}, err
	}
	path.Kind, path.Table = KindTable, table
	if len(resolved) == 2 {
		return path, nil
	}

	key, err := decodeKey(resolved[2])
	if err != nil {
		return Path{}, err
	}
	path.Kind, path.Key = KindRow, key
	if len(resolved) == 3 {
		return path, nil
	}

	field, err := decodeSegment(resolved[3])
	if err != nil {
		return Path{}, err
	}
	path.Kind, path.Field = KindField, field
	return path, nil
}

// Canonical renders the path in its canonical absolute form. It reports an
// error only for a Path built by hand with a key the MVP cannot navigate;
// Parse never returns such a value.
func (p Path) Canonical() (string, error) {
	if p.Kind == KindRoot {
		return "/", nil
	}
	parts := make([]string, 0, 4)
	if err := checkAlias(p.Alias); err != nil {
		return "", err
	}
	parts = append(parts, p.Alias)
	if p.Kind == KindMount {
		return "/" + strings.Join(parts, "/"), nil
	}
	parts = append(parts, encodeSegment(p.Table))
	if p.Kind == KindTable {
		return "/" + strings.Join(parts, "/"), nil
	}
	key, err := encodeKey(p.Key)
	if err != nil {
		return "", err
	}
	parts = append(parts, key)
	if p.Kind == KindRow {
		return "/" + strings.Join(parts, "/"), nil
	}
	parts = append(parts, encodeSegment(p.Field))
	return "/" + strings.Join(parts, "/"), nil
}

func decodeAlias(raw string) (string, error) {
	alias, err := decodeSegment(raw)
	if err != nil {
		return "", err
	}
	if err := checkAlias(alias); err != nil {
		return "", err
	}
	return alias, nil
}

// ValidAlias reports whether alias is a legal mount alias (DESIGN §4: ASCII
// [A-Za-z0-9_-]+). The registry uses it to enforce the same rule as the path
// codec.
func ValidAlias(alias string) bool {
	return checkAlias(alias) == nil
}

// checkAlias enforces DESIGN §4: ASCII [A-Za-z0-9_-]+.
func checkAlias(alias string) error {
	if alias == "" {
		return invalid("alias is empty")
	}
	for i := 0; i < len(alias); i++ {
		c := alias[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return invalid("alias %q is not [A-Za-z0-9_-]+", alias)
	}
	return nil
}

// decodeSegment percent-decodes one raw segment and rejects anything whose
// canonical encoding differs from the input.
func decodeSegment(raw string) (string, error) {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(raw) {
			return "", invalid("truncated percent escape")
		}
		hi, ok := unhex(raw[i+1])
		if !ok {
			return "", invalid("invalid percent escape")
		}
		lo, ok := unhex(raw[i+2])
		if !ok {
			return "", invalid("invalid percent escape")
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	decoded := b.String()
	if strings.IndexByte(decoded, 0) >= 0 {
		return "", invalid("NUL byte in path")
	}
	if encodeSegment(decoded) != raw {
		return "", invalid("non-canonical percent encoding")
	}
	return decoded, nil
}

// unhex accepts uppercase hex only; lowercase hex is a non-canonical escape.
func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// encodeSegment percent-encodes every byte outside [A-Za-z0-9._-]. "%" and "/"
// are outside, "~" is reserved for key markers, and a segment that is exactly
// "." or ".." escapes its dots so it cannot be read as traversal.
func encodeSegment(s string) string {
	escapeDots := s == "." || s == ".."
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isLiteral(c) && !(escapeDots && c == '.') {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

func isLiteral(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.'
}

const (
	emptyKeyMarker     = "~e"
	compositeKeyPrefix = "~k"
)

// decodeKey parses the key segment. A plain segment is typed as text because
// the path alone cannot tell a TEXT "42" from an INTEGER 42; the catalog layer
// resolves that with the declared primary-key type.
func decodeKey(raw string) (storage.Key, error) {
	switch {
	case raw == emptyKeyMarker:
		return storage.Key{{Kind: kindText, Data: ""}}, nil
	case strings.HasPrefix(raw, compositeKeyPrefix):
		return decodeCompositeKey(strings.TrimPrefix(raw, compositeKeyPrefix))
	case strings.HasPrefix(raw, "~"):
		return nil, invalid("unknown key marker")
	}
	text, err := decodeSegment(raw)
	if err != nil {
		return nil, err
	}
	return storage.Key{{Kind: kindText, Data: text}}, nil
}

func decodeCompositeKey(encoded string) (storage.Key, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) == 0 {
		return nil, invalid("composite key is not base64url without padding")
	}
	var key storage.Key
	if err := json.Unmarshal(payload, &key); err != nil {
		return nil, invalid("composite key is not a JSON array")
	}
	if len(key) == 0 {
		return nil, invalid("composite key is empty")
	}
	if len(key) == 1 {
		// A one-value identity has a simple representation, so accepting a
		// composite would give two spellings for the same key.
		return nil, invalid("composite key needs at least two values")
	}
	// Re-marshaling is the canonicality check: it rejects whitespace, field
	// order, duplicate keys, unknown fields and non-canonical escapes.
	canonical, err := json.Marshal(key)
	if err != nil { // impossible: Value holds only strings
		return nil, invalid("composite key is not serializable")
	}
	if string(canonical) != string(payload) {
		return nil, invalid("composite key is not canonical JSON")
	}
	for _, v := range key {
		if err := checkKeyValue(v); err != nil {
			return nil, err
		}
	}
	return key, nil
}

// checkKeyValue accepts only the identity kinds the MVP can navigate (DESIGN
// §4): real, blob and null identities are not paths.
func checkKeyValue(v storage.Value) error {
	switch v.Kind {
	case kindText:
		// decodeSegment rejects NUL, so encoding it would produce a path the
		// codec cannot parse back; reject it where every encode/decode path
		// (simple, composite, and composite decode) goes through.
		if strings.IndexByte(v.Data, 0) >= 0 {
			return invalid("text key contains NUL")
		}
		return nil
	case kindInteger:
		if !isCanonicalInteger(v.Data) {
			return invalid("integer key is not canonical decimal")
		}
		return nil
	}
	return invalid("key kind %q is not navigable", v.Kind)
}

// encodeKey renders a key as one path segment.
func encodeKey(key storage.Key) (string, error) {
	if len(key) == 0 {
		return "", invalid("key is empty")
	}
	if len(key) > 1 {
		return encodeCompositeKey(key)
	}
	only := key[0]
	if err := checkKeyValue(only); err != nil {
		return "", err
	}
	switch only.Kind {
	case kindText:
		if only.Data == "" {
			return emptyKeyMarker, nil
		}
		return encodeSegment(only.Data), nil
	}
	return only.Data, nil
}

func encodeCompositeKey(key storage.Key) (string, error) {
	for _, v := range key {
		if err := checkKeyValue(v); err != nil {
			return "", err
		}
		// JSON cannot represent arbitrary bytes losslessly: invalid UTF-8
		// would be replaced silently and the identity would change.
		if v.Kind == kindText && !utf8.ValidString(v.Data) {
			return "", invalid("text key is not valid UTF-8 in a composite key")
		}
	}
	// json.Marshal escapes <, > and & as \u003c… by default; deterministic and
	// matched by the decoder's re-marshal check, so this form is the canonical
	// one for ~k even though it is not minimal JSON (DESIGN §4).
	payload, err := json.Marshal(key)
	if err != nil { // impossible: Value holds only strings
		return "", invalid("composite key is not serializable")
	}
	return compositeKeyPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// isCanonicalInteger accepts only the decimal forms strconv emits: no sign for
// non-negatives, no leading zeros, no "+" prefix.
func isCanonicalInteger(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return false
	}
	return strconv.FormatInt(n, 10) == s
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPath, fmt.Sprintf(format, args...))
}
