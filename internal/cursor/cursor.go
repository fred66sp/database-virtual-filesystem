// Package cursor encodes and signs keyset pagination positions (DESIGN §7).
//
// A signed cursor is base64url(payload) "." base64url(HMAC-SHA256(payload)).
// The payload carries the version, mount, table, filters, order, search text,
// projection and last key, so a client cannot reuse a cursor across queries or
// edit the position without being rejected as E_USAGE. Signing is not
// encryption: values the policy hides never enter a cursor the client is
// allowed to receive, because an unauthorized order is refused before the page
// is served.
package cursor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"dbvfs/internal/storage"
)

// Version is the only cursor schema this build accepts.
const Version = 1

// State is the signed payload. Filters, Order and Fields bind a cursor to one
// query.
type State struct {
	Version int                 `json:"v"`
	Mount   string              `json:"mount"`
	Table   string              `json:"table"`
	Filters []storage.Filter    `json:"filters"`
	Order   []storage.OrderSpec `json:"order"`
	Search  string              `json:"search,omitempty"`
	Fields  []string            `json:"fields,omitempty"`
	LastKey storage.Key         `json:"last_key"`
}

// Sign returns a tamper-evident cursor for s.
func Sign(key []byte, s State) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: cursor key is empty", storage.ErrInvalid)
	}
	s.Version = Version
	payload, err := json.Marshal(s)
	if err != nil { // impossible: State holds only strings and slices
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify authenticates and decodes a cursor. Any malformed, truncated or
// tampered token is storage.ErrInvalid, which the CLI maps to E_USAGE.
func Verify(key []byte, token string) (State, error) {
	if len(key) == 0 {
		return State{}, fmt.Errorf("%w: cursor key is empty", storage.ErrInvalid)
	}
	payloadPart, macPart, ok := strings.Cut(token, ".")
	if !ok || payloadPart == "" || macPart == "" {
		return State{}, fmt.Errorf("%w: cursor is not base64url(payload).base64url(mac)", storage.ErrInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return State{}, fmt.Errorf("%w: cursor payload is not base64url", storage.ErrInvalid)
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil {
		return State{}, fmt.Errorf("%w: cursor signature is not base64url", storage.ErrInvalid)
	}
	want := hmac.New(sha256.New, key)
	want.Write(payload)
	if !hmac.Equal(gotMAC, want.Sum(nil)) {
		return State{}, fmt.Errorf("%w: cursor signature does not match", storage.ErrInvalid)
	}
	var s State
	if err := json.Unmarshal(payload, &s); err != nil {
		return State{}, fmt.Errorf("%w: cursor payload is not JSON", storage.ErrInvalid)
	}
	if s.Version != Version {
		return State{}, fmt.Errorf("%w: unsupported cursor version %d", storage.ErrInvalid, s.Version)
	}
	if len(s.LastKey) == 0 {
		return State{}, fmt.Errorf("%w: cursor has no last key", storage.ErrInvalid)
	}
	return s, nil
}

// EncodeKey is the unsigned position the storage adapter receives in
// Query.Cursor. Only the application layer signs what leaves the process.
func EncodeKey(key storage.Key) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: empty pagination key", storage.ErrInvalid)
	}
	payload, err := json.Marshal(key)
	if err != nil { // impossible: Value holds only strings
		return "", fmt.Errorf("encode pagination key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeKey reads the position the application put in Query.Cursor.
func DecodeKey(token string) (storage.Key, error) {
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: pagination key is not base64url", storage.ErrInvalid)
	}
	var key storage.Key
	if err := json.Unmarshal(payload, &key); err != nil {
		return nil, fmt.Errorf("%w: pagination key is not JSON", storage.ErrInvalid)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: empty pagination key", storage.ErrInvalid)
	}
	canonical, err := json.Marshal(key)
	if err != nil { // impossible: Value holds only strings
		return nil, fmt.Errorf("%w: pagination key is not canonical", storage.ErrInvalid)
	}
	if string(canonical) != string(payload) {
		return nil, fmt.Errorf("%w: pagination key is not canonical", storage.ErrInvalid)
	}
	return key, nil
}
