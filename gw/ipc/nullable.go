package ipc

import "encoding/json"

// Nullable is an optional field whose value may also be JSON null (TypeScript `T|null` behind `[name]`): absent,
// null and a value all survive a decode and an encode. Tag it `json:",omitzero"`.
type Nullable[T any] struct {
	V    T
	Null bool
	Set  bool
}

// Some is a present, non-null value.
func Some[T any](v T) Nullable[T] { return Nullable[T]{V: v, Set: true} }

// Null is a present null.
func Null[T any]() Nullable[T] { return Nullable[T]{Null: true, Set: true} }

// IsZero reports an absent field, so omitzero leaves it out.
func (n Nullable[T]) IsZero() bool { return !n.Set }

// MarshalJSON writes the value, or null.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.V)
}

// UnmarshalJSON reads a value or null; encoding/json calls it for a JSON null too.
func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	*n = Nullable[T]{Set: true}
	if string(b) == "null" {
		n.Null = true
		return nil
	}
	return json.Unmarshal(b, &n.V)
}
