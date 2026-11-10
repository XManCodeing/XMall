package application

import (
	"testing"
	"time"
)

// TestExportTimeUsesEastEight 验证 CSV 时间跨 UTC 日期、空值和存储 UTC 归一化互不干扰。
func TestExportTimeUsesEastEight(t *testing.T) {
	instant := time.Date(2026, 12, 31, 20, 30, 0, 0, time.UTC)
	if actual := formatNullableTime(&instant); actual != "2027-01-01T04:30:00+08:00" {
		t.Fatalf("export time = %q", actual)
	}
	if formatNullableTime(nil) != "" {
		t.Fatal("nil export time must be empty")
	}
	stored := normalizeExpireAt(&instant)
	if stored == nil || !stored.Equal(instant) || stored.Location() != time.UTC {
		t.Fatalf("storage instant changed: %v", stored)
	}
}

func TestNormalizeIDsDeduplicatesAndDropsZero(t *testing.T) {
	got := normalizeIDs([]uint{0, 2, 2, 3, 0, 3})
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("unexpected ids: %#v", got)
	}
}

func TestNormalizeExpireAtUTC(t *testing.T) {
	raw := time.Date(2026, 7, 21, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	got := normalizeExpireAt(&raw)
	if got == nil || got.Location() != time.UTC {
		t.Fatalf("expected UTC expire at, got %#v", got)
	}
}
