package domain_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
)

// TestTermMonthsAndOverflow 验证非固定档位、永久和所有边界值，参数由测试表提供。
func TestTermMonthsAndOverflow(t *testing.T) {
	for _, months := range []int64{2, 5, 18, d.MaxMonths} {
		seconds, err := d.DurationSeconds(d.TermMonths, months)
		if err != nil || seconds != months*30*24*3600 {
			t.Fatalf("months=%d seconds=%d err=%v", months, seconds, err)
		}
	}
	for _, months := range []int64{0, -1, d.MaxMonths + 1, math.MaxInt64} {
		if _, err := d.DurationSeconds(d.TermMonths, months); err == nil {
			t.Fatalf("accepted invalid months %d", months)
		}
	}
	if seconds, err := d.DurationSeconds(d.TermPermanent, 0); err != nil || seconds != 0 {
		t.Fatalf("permanent: %d %v", seconds, err)
	}
	if _, err := d.DurationSeconds(d.TermPermanent, 999); err == nil {
		t.Fatal("permanent sentinel accepted")
	}
	var term d.SKUTerm
	for _, raw := range []string{`{"months":2.5}`, `{"months":"2"}`, `{"months":9223372036854775808}`} {
		if err := json.Unmarshal([]byte(raw), &term); err == nil {
			t.Fatalf("accepted invalid JSON months %s", raw)
		}
	}
}

// TestExpiryUTCAndDateOverflow 验证固定三十天、UTC 及日期上限，不替 B5 选择起算点。
func TestExpiryUTCAndDateOverflow(t *testing.T) {
	start := time.Date(2026, 1, 31, 12, 0, 0, 0, time.FixedZone("test", 8*3600))
	end, err := d.Expiry(start, d.TermMonths, 2)
	if err != nil || end.Sub(start) != 60*24*time.Hour || end.Location() != time.UTC {
		t.Fatalf("expiry: %v %v", end, err)
	}
	if end, err := d.Expiry(start, d.TermPermanent, 0); err != nil || end != nil {
		t.Fatalf("permanent expiry: %v %v", end, err)
	}
	if _, err := d.Expiry(time.Date(9999, 12, 1, 0, 0, 0, 0, time.UTC), d.TermMonths, 2); err == nil {
		t.Fatal("date overflow accepted")
	}
	if _, err := d.Expiry(time.Time{}, d.TermMonths, 2); err == nil {
		t.Fatal("zero start accepted")
	}
}
