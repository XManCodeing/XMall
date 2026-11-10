package jsonslice

import (
	"reflect"
	"testing"
)

// TestTextScans covers migrated SQLite TEXT arrays and rejects unsupported driver values.
// t uses synthetic image/tag and identifier lists without accessing external storage.
func TestTextScans(t *testing.T) {
	var stringsValue Strings
	if err := stringsValue.Scan(`["image.png","tag"]`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stringsValue, Strings{"image.png", "tag"}) {
		t.Fatal("text strings lost")
	}
	var uintsValue Uints
	if err := uintsValue.Scan(`[1,2]`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uintsValue, Uints{1, 2}) {
		t.Fatal("text identifiers lost")
	}
	if err := stringsValue.Scan(12); err == nil {
		t.Fatal("unsupported strings input accepted")
	}
	if err := uintsValue.Scan(true); err == nil {
		t.Fatal("unsupported identifiers input accepted")
	}
}

func TestStringsValueAndScan(t *testing.T) {
	value, err := (Strings{"alpha", "beta"}).Value()
	if err != nil {
		t.Fatalf("encode strings: %v", err)
	}
	var decoded Strings
	if err := decoded.Scan(value); err != nil {
		t.Fatalf("scan strings: %v", err)
	}
	if !reflect.DeepEqual(decoded, Strings{"alpha", "beta"}) {
		t.Fatalf("unexpected strings: %#v", decoded)
	}
	if err := decoded.Scan(nil); err != nil || decoded == nil || len(decoded) != 0 {
		t.Fatalf("nil scan must yield an empty non-nil slice: %#v err=%v", decoded, err)
	}
}

func TestUintsValueAndScan(t *testing.T) {
	value, err := (Uints{1, 2}).Value()
	if err != nil {
		t.Fatalf("encode uints: %v", err)
	}
	var decoded Uints
	if err := decoded.Scan(value); err != nil {
		t.Fatalf("scan uints: %v", err)
	}
	if !reflect.DeepEqual(decoded, Uints{1, 2}) {
		t.Fatalf("unexpected uints: %#v", decoded)
	}
}
