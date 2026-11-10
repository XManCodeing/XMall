package application

import (
	"errors"
	"testing"
	"time"

	reportingdomain "github.com/XManCodeing/XMall/internal/modules/reporting/domain"
)

func TestResolveTodayInRequestedTimezone(t *testing.T) {
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	window, err := Resolve(reportingdomain.Query{Range: "today", Timezone: "Asia/Shanghai"}, now)
	if err != nil {
		t.Fatalf("resolve today: %v", err)
	}
	if got := window.StartAt.Format(time.RFC3339); got != "2026-07-21T00:00:00+08:00" {
		t.Fatalf("start want 2026-07-21T00:00:00+08:00, got %s", got)
	}
	if window.EndAt.Sub(window.StartAt) != 24*time.Hour {
		t.Fatalf("today duration want 24h, got %s", window.EndAt.Sub(window.StartAt))
	}
}

// TestResolveTodayIgnoresHostAndRequestedTimezone 验证统一东八区跨 UTC 日期和错误时区输入。
func TestResolveTodayIgnoresHostAndRequestedTimezone(t *testing.T) {
	instant := time.Date(2026, 12, 31, 20, 30, 0, 0, time.UTC)
	for _, timezone := range []string{"", "UTC", "America/New_York", "invalid/timezone"} {
		window, err := Resolve(reportingdomain.Query{Range: "today", Timezone: timezone}, instant)
		if err != nil {
			t.Fatalf("timezone %q: %v", timezone, err)
		}
		if window.Timezone != "Asia/Shanghai" || window.StartAt.Format(time.RFC3339) != "2027-01-01T00:00:00+08:00" {
			t.Fatalf("timezone %q: %+v", timezone, window)
		}
		if !window.StartAt.Equal(time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC)) {
			t.Fatalf("wrong absolute boundary: %v", window.StartAt)
		}
	}
}

func TestResolveRejectsOversizedCustomRange(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, CustomMaxDays+1)
	_, err := Resolve(reportingdomain.Query{Range: "custom", From: &from, To: &to, Timezone: "UTC"}, from)
	if !errors.Is(err, reportingdomain.ErrRangeInvalid) {
		t.Fatalf("want ErrRangeInvalid, got %v", err)
	}
}
