package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestPageQuery validates defaults, supported bounds and integer overflow without changing cursor JSON.
func TestPageQuery(t *testing.T) {
	for _, query := range []PageQuery{{}, {1, 10}, {2, 20}, {3, 50}, {4, 100}} {
		actual, err := query.Normalize()
		if err != nil || actual.Page < 1 || actual.PageSize < 1 {
			t.Fatalf("normalize %+v: %+v %v", query, actual, err)
		}
	}
	for _, query := range []PageQuery{{-1, 20}, {1, -1}, {1, 101}, {int(^uint(0) >> 1), 100}} {
		if _, err := query.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", query, err)
		}
	}
	legacy, err := json.Marshal(TaskPage{NextAfterID: 9007199254740993})
	if err != nil || strings.Contains(string(legacy), "total") || !strings.Contains(string(legacy), `"next_after_id":"9007199254740993"`) {
		t.Fatalf("legacy contract changed: %s %v", legacy, err)
	}
	encoded, err := json.Marshal(StepPage{PageInfo: &PageInfo{Page: 2, PageSize: 10, Total: 15, TotalPage: 2}})
	if err != nil || !strings.Contains(string(encoded), `"total":15`) || !strings.Contains(string(encoded), `"page_size":10`) {
		t.Fatalf("missing numbered metadata: %s %v", encoded, err)
	}
}
