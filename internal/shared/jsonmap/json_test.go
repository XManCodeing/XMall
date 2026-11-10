package jsonmap

import (
	"reflect"
	"testing"
)

// TestJSONScanTextAndBytes covers MySQL bytes and migrated SQLite TEXT values.
// t owns synthetic inputs; stale keys must not survive scanning a new row.
func TestJSONScanTextAndBytes(t *testing.T) {
	for _, source := range []any{`{"zh-CN":"开始购物","count":2}`, []byte(`{"zh-CN":"开始购物","count":2}`)} {
		value := JSON{"stale": true}
		if err := value.Scan(source); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, JSON{"zh-CN": "开始购物", "count": float64(2)}) {
			t.Fatalf("decoded value mismatch: %#v", value)
		}
	}
}

// TestJSONScanRejectsInvalidValues prevents silent missing labels and partial mutations.
// t supplies malformed and unsupported values; errors must preserve the previous object.
func TestJSONScanRejectsInvalidValues(t *testing.T) {
	for _, source := range []any{42, true, `{`, `[]`, `"text"`} {
		value := JSON{"previous": true}
		if err := value.Scan(source); err == nil {
			t.Fatalf("expected error for %T", source)
		}
		if !reflect.DeepEqual(value, JSON{"previous": true}) {
			t.Fatal("failed scan changed previous object")
		}
	}
}

func TestJSONDatabaseRoundTrip(t *testing.T) {
	original := JSON{
		"enabled": true,
		"name":    "storefront",
		"nested":  map[string]interface{}{"count": float64(2)},
	}

	encoded, err := original.Value()
	if err != nil {
		t.Fatalf("encode JSON value: %v", err)
	}

	var decoded JSON
	if err := decoded.Scan(encoded); err != nil {
		t.Fatalf("scan JSON value: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\ngot:  %#v", original, decoded)
	}
}

func TestJSONScanNilCreatesEmptyObject(t *testing.T) {
	value := JSON{"stale": true}
	if err := value.Scan(nil); err != nil {
		t.Fatalf("scan nil JSON: %v", err)
	}
	if value == nil || len(value) != 0 {
		t.Fatalf("scan nil should create an empty object, got %#v", value)
	}
}

func TestJSONNilDatabaseValueStaysNull(t *testing.T) {
	var value JSON
	encoded, err := value.Value()
	if err != nil {
		t.Fatalf("encode nil JSON: %v", err)
	}
	if encoded != nil {
		t.Fatalf("nil JSON should produce a NULL driver value, got %#v", encoded)
	}
}
