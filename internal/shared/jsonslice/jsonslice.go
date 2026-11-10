package jsonslice

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// scanBytes accepts SQL text or byte values and returns JSON bytes without logging contents.
// Unsupported driver values return a typed error instead of silently dropping array fields.
func scanBytes(value interface{}) ([]byte, error) {
	switch encoded := value.(type) {
	case []byte:
		return encoded, nil
	case string:
		return []byte(encoded), nil
	default:
		return nil, fmt.Errorf("jsonslice: unsupported database value type %T", value)
	}
}

// Strings is a JSON-backed string slice suitable for SQL value/scan contracts.
type Strings []string

func (items Strings) Value() (driver.Value, error) {
	if items == nil {
		return nil, nil
	}
	return json.Marshal(items)
}

func (items *Strings) Scan(value interface{}) error {
	if value == nil {
		*items = Strings{}
		return nil
	}
	data, err := scanBytes(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, items)
}

// Uints is a JSON-backed unsigned-integer slice suitable for SQL value/scan contracts.
type Uints []uint

func (items Uints) Value() (driver.Value, error) {
	if items == nil {
		return nil, nil
	}
	return json.Marshal(items)
}

func (items *Uints) Scan(value interface{}) error {
	if value == nil {
		*items = Uints{}
		return nil
	}
	data, err := scanBytes(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, items)
}
