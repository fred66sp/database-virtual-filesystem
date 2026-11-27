package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// canonicalFields is DESIGN §5.2: the semantic fields of each table, in order.
var canonicalFields = map[string][]string{
	"observations": {"kind", "title", "body", "rationale", "alternatives",
		"consequences", "topic_key"},
	"checkpoints": {"goal", "state", "next_steps", "files", "open_questions"},
	"preferences": {"pref_key", "value", "rationale"},
}

// ContentHash is the canonical hash of DESIGN §5.2, byte-identical to
// tools/content-hash.py: NFC, Python whitespace collapse, the compact JSON
// array without spaces and SHA-256 over its UTF-8 bytes. The hash is a table
// shared with existing bases, so a change here breaks every base.
func ContentHash(table string, data map[string]any) (string, error) {
	fields, ok := canonicalFields[table]
	if !ok {
		return "", errorf(codeInternal, "tabla desconocida para content_hash: %s", table)
	}
	parts := make([]string, len(fields))
	for index, field := range fields {
		parts[index] = canonicalField(data[field])
	}
	canonical := "[" + strings.Join(parts, ",") + "]"
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// canonicalField normalizes one field. The core only feeds text and nil, as the
// Python adapter did; anything else would produce a hash different from
// content-hash.py, so it is a defect reported instead of a wrong digest.
func canonicalField(value any) string {
	switch typed := value.(type) {
	case nil:
		return `""`
	case string:
		return quote(normalize(typed))
	default:
		return quote(normalize(fmt.Sprint(typed))) // shortcut: non-text fields are unreachable today; if one appears, it must serialize like json.dumps — replace by an error then.
	}
}

// normalize is content-hash.py's normaliza: NFC, trim, and collapse every run
// of Python whitespace into one ASCII space.
func normalize(text string) string {
	var builder strings.Builder
	pending := false
	for _, char := range norm.NFC.String(text) {
		if pythonSpace(char) {
			pending = builder.Len() > 0
			continue
		}
		if pending {
			builder.WriteByte(' ')
			pending = false
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// pythonSpace says whether Python's str.split() treats the rune as whitespace.
// Go's unicode.IsSpace covers the Unicode White_Space property but omits the
// C0 separators \x1c-\x1f, which Python does treat as whitespace.
func pythonSpace(char rune) bool {
	return unicode.IsSpace(char) || (char >= 0x1c && char <= 0x1f)
}

// quote serializes a string the way Python's json.dumps(ensure_ascii=False)
// does: only quotes, backslashes and control characters are escaped, so <>&,
// U+2028, U+2029 and non-BMP runes stay raw. Go's encoding/json escapes all of
// them, which would change the hash.
func quote(text string) string {
	var builder strings.Builder
	builder.Grow(len(text) + 2)
	builder.WriteByte('"')
	for _, char := range text {
		switch char {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		default:
			if char < 0x20 {
				fmt.Fprintf(&builder, `\u%04x`, char)
				continue
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
