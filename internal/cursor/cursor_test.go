package cursor

import (
	"errors"
	"strings"
	"testing"

	"dbvfs/internal/storage"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func sample() State {
	return State{
		Mount:   "work",
		Table:   "tasks",
		Filters: []storage.Filter{{Column: "id", Op: ">=", Value: storage.Value{Kind: "integer", Data: "2"}}},
		Order:   []storage.OrderSpec{{Column: "id"}},
		LastKey: storage.Key{{Kind: "integer", Data: "2"}},
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	token, err := Sign(testKey, sample())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := Verify(testKey, token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Version != Version || got.Mount != "work" || got.Table != "tasks" {
		t.Errorf("decoded cursor = %+v, want version %d with work/tasks", got, Version)
	}
	if len(got.Filters) != 1 || got.Filters[0].Column != "id" || got.Filters[0].Op != ">=" {
		t.Errorf("filters = %+v, want the signed id >= 2 filter", got.Filters)
	}
	if len(got.LastKey) != 1 || got.LastKey[0].Data != "2" {
		t.Errorf("last key = %+v, want integer 2", got.LastKey)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	token, err := Sign(testKey, sample())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("token = %q, want payload.mac", token)
	}

	tests := []struct {
		name  string
		token string
		key   []byte
	}{
		{"tampered payload", strings.Replace(token, parts[0], parts[0][:len(parts[0])-1]+"A", 1), testKey},
		{"tampered mac", parts[0] + "." + parts[1][:len(parts[1])-1] + "A", testKey},
		{"wrong key", token, []byte("ffffffffffffffffffffffffffffffff")},
		{"garbage", "not-a-cursor", testKey},
		{"empty", "", testKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Verify(tc.key, tc.token); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("Verify error = %v, want storage.ErrInvalid", err)
			}
		})
	}
	if _, err := Sign(nil, sample()); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("Sign without a key error = %v, want storage.ErrInvalid", err)
	}
}

func TestKeyPosition(t *testing.T) {
	key := storage.Key{{Kind: "text", Data: "a"}, {Kind: "integer", Data: "42"}}
	token, err := EncodeKey(key)
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}
	got, err := DecodeKey(token)
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if len(got) != 2 || got[0] != key[0] || got[1] != key[1] {
		t.Errorf("decoded key = %+v, want %+v", got, key)
	}
	if _, err := DecodeKey(""); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("DecodeKey(empty) error = %v, want storage.ErrInvalid", err)
	}
	if _, err := DecodeKey("e30"); !errors.Is(err, storage.ErrInvalid) { // {}
		t.Errorf("DecodeKey({}) error = %v, want storage.ErrInvalid", err)
	}
}
