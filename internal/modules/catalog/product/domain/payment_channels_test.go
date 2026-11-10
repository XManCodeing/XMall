package productdomain

import (
	"reflect"
	"testing"
)

// TestWholesalePriceTextScan preserves exact prices from migrated SQLite TEXT columns.
// t supplies equivalent byte/text inputs and checks malformed types fail closed.
func TestWholesalePriceTextScan(t *testing.T) {
	payload := `[{"sku_id":3,"min_quantity":2,"unit_price":"12.34"}]`
	for _, source := range []any{payload, []byte(payload)} {
		var tiers WholesalePriceTiers
		if err := tiers.Scan(source); err != nil {
			t.Fatal(err)
		}
		if len(tiers) != 1 || tiers[0].SKUID != 3 || tiers[0].MinQuantity != 2 || tiers[0].UnitPrice.String() != "12.34" {
			t.Fatal("wholesale price lost")
		}
	}
	var tiers WholesalePriceTiers
	if err := tiers.Scan(1); err == nil {
		t.Fatal("unsupported value silently accepted")
	}
}

func TestPaymentChannelIDsRoundTripAndDiscardInvalidValues(t *testing.T) {
	encoded := EncodePaymentChannelIDs([]uint{3, 7})
	if encoded != "[3,7]" {
		t.Fatalf("encoded IDs = %q", encoded)
	}
	if got := DecodePaymentChannelIDs(encoded); !reflect.DeepEqual(got, []uint{3, 7}) {
		t.Fatalf("decoded IDs = %#v", got)
	}
	if got := DecodePaymentChannelIDs(`[0,3,0,7]`); !reflect.DeepEqual(got, []uint{3, 7}) {
		t.Fatalf("filtered IDs = %#v", got)
	}
	for _, raw := range []string{"", "[]", "invalid", "[0]"} {
		if got := DecodePaymentChannelIDs(raw); got != nil {
			t.Fatalf("DecodePaymentChannelIDs(%q) = %#v, want nil", raw, got)
		}
	}
}
