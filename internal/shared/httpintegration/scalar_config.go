package httpintegration

import (
	"encoding/json"
	"errors"

	"github.com/shopspring/decimal"
)

// optionalScalar keeps JSON presence and type separate from the legacy exported
// string fields. Existing Go callers can still construct and normalize strings.
// Numbers never pass through float64; null is explicit, not an omitted value.
type optionalScalar struct {
	present bool
	value   any
}

func readScalar(raw json.RawMessage) (optionalScalar, string, error) {
	if len(raw) == 0 {
		return optionalScalar{}, "", nil
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return optionalScalar{}, "", err
	}
	s := optionalScalar{present: true, value: v}
	switch x := v.(type) {
	case nil, bool:
		return s, "", nil
	case string:
		return s, x, nil
	case json.Number:
		if _, err := scalarNumber(x); err != nil {
			return optionalScalar{}, "", err
		}
		return s, "", nil
	default:
		return optionalScalar{}, "", errors.New("configuration constant must be a JSON scalar")
	}
}

func scalarNumber(n json.Number) (decimal.Decimal, error) {
	if len(n) > 256 {
		return decimal.Decimal{}, errors.New("number exceeds precision limit")
	}
	d, err := decimal.NewFromString(string(n))
	if err != nil || d.Exponent() < -1000 || d.Exponent() > 1000 {
		return decimal.Decimal{}, errors.New("invalid or excessive number")
	}
	return d, nil
}

func (s optionalScalar) get(legacy string) (any, bool) {
	// Always use the public string for strings so credential normalization by
	// existing callers is respected. Typed scalars have no string placeholder.
	if _, ok := s.value.(string); ok || legacy != "" {
		return legacy, true
	}
	return s.value, s.present
}

func scalarJSON(s optionalScalar, legacy string) json.RawMessage {
	v, present := s.get(legacy)
	if !present {
		return nil
	}
	b, _ := json.Marshal(v) // only scalars validated by readScalar reach here
	return b
}

func (c *Condition) UnmarshalJSON(data []byte) error {
	var wire struct {
		Path   string          `json:"path"`
		Equals json.RawMessage `json:"equals"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	s, text, err := readScalar(wire.Equals)
	if err != nil {
		return err
	}
	// Old Go serialization wrote {path:"",equals:""} for no condition.
	if wire.Path == "" && s.value == "" {
		s = optionalScalar{}
	}
	*c = Condition{Path: wire.Path, Equals: text, equals: s}
	return nil
}

func (c Condition) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path   string          `json:"path"`
		Equals json.RawMessage `json:"equals,omitempty"`
	}{c.Path, scalarJSON(c.equals, c.Equals)})
}

func (f *Field) UnmarshalJSON(data []byte) error {
	type plain Field
	var wire struct {
		plain
		Value   json.RawMessage `json:"value"`
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	value, text, err := readScalar(wire.Value)
	if err != nil {
		return err
	}
	fallback, fallbackText, err := readScalar(wire.Default)
	if err != nil {
		return err
	}
	*f = Field(wire.plain)
	// The old encoder always emitted BOTH empty placeholders for path fields.
	// Treat that pair as unset; a new explicit empty default (value omitted)
	// remains present. This avoids turning old required fields into empty values.
	if f.Path != "" && value.present && value.value == "" && fallback.present && fallback.value == "" {
		value, fallback = optionalScalar{}, optionalScalar{}
	}
	f.Value, f.value = text, value
	f.Default, f.fallback = fallbackText, fallback
	return nil
}

func (f Field) MarshalJSON() ([]byte, error) {
	type plain Field
	return json.Marshal(struct {
		plain
		Value   json.RawMessage `json:"value,omitempty"`
		Default json.RawMessage `json:"default,omitempty"`
	}{plain(f), scalarJSON(f.value, f.Value), scalarJSON(f.fallback, f.Default)})
}

// String conditions retain historical scalar-text matching ("0" matches 0).
// New non-string conditions are type-strict: false never matches "false" or 0.
func matchesScalar(actual, expected any) bool {
	switch x := expected.(type) {
	case string:
		s, err := scalar(actual)
		return err == nil && s == x
	case bool:
		v, ok := actual.(bool)
		return ok && v == x
	case nil:
		return actual == nil
	case json.Number:
		v, ok := actual.(json.Number)
		if !ok {
			return false
		}
		a, errA := scalarNumber(v)
		b, errB := scalarNumber(x)
		return errA == nil && errB == nil && a.Equal(b)
	default:
		return false
	}
}
