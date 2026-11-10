// Package jsonmap provides the shared JSON object value used at persistence
// and transport boundaries. It is deliberately independent from GORM models
// and business modules so bounded contexts do not need to import a global
// models package merely to exchange arbitrary JSON objects.
package jsonmap

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON is a JSON object with database/sql serialization support.
type JSON map[string]interface{}

// Value implements driver.Valuer.
func (value JSON) Value() (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

// Scan decodes database JSON from byte or text drivers into a fresh object.
// source may be NULL, []byte, or string. Invalid types/JSON return an error
// without retaining partially decoded fields or disclosing database contents.
func (value *JSON) Scan(source interface{}) error {
	if source == nil {
		*value = make(JSON)
		return nil
	}
	var data []byte
	switch encoded := source.(type) {
	case []byte:
		data = encoded
	case string:
		// SQLite TEXT columns return strings, while MySQL and SQLite BLOB return bytes.
		data = []byte(encoded)
	default:
		return fmt.Errorf("jsonmap: unsupported database value type %T", source)
	}
	var decoded JSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("jsonmap: decode database JSON: %w", err)
	}
	*value = decoded
	return nil
}
