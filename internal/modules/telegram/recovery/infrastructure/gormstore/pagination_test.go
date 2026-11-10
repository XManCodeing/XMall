package gormstore_test

import (
	"errors"
	"fmt"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// TestNumberedTasks exercises bounded direct jumps, exact filtered counts and the unchanged cursor path.
func TestNumberedTasks(t *testing.T) {
	h := setup(t)
	for index := 0; index < 25; index++ {
		preflight(t, h, fmt.Sprintf("page-%d", index))
	}
	page, err := h.store.ListPage(h.ctx, c.ListFilter{Kind: d.GroupReplace, State: d.AwaitingConfirmation}, c.PageQuery{Page: 3, PageSize: 10})
	if err != nil || page.Total != 25 || page.TotalPage != 3 || len(page.Items) != 5 || page.Items[0].ID != 21 {
		t.Fatalf("jump: %+v %v", page, err)
	}
	page, err = h.store.ListPage(h.ctx, c.ListFilter{}, c.PageQuery{Page: 99, PageSize: 20})
	if err != nil || page.Page != 2 || page.Total != 25 || len(page.Items) != 5 {
		t.Fatalf("clamp: %+v %v", page, err)
	}
	page, err = h.store.ListPage(h.ctx, c.ListFilter{State: d.Completed}, c.PageQuery{})
	if err != nil || page.Total != 0 || page.Page != 1 || page.TotalPage != 1 || len(page.Items) != 0 {
		t.Fatalf("empty filter: %+v %v", page, err)
	}
	for _, filter := range []c.ListFilter{{Kind: "invalid"}, {State: "invalid"}, {AfterID: 1}, {Limit: 20}} {
		if _, err := h.store.ListPage(h.ctx, filter, c.PageQuery{}); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted filter %+v: %v", filter, err)
		}
	}
	cursor, err := h.store.List(h.ctx, c.ListFilter{Limit: 10})
	if err != nil || cursor.NextAfterID != 10 || cursor.PageInfo != nil {
		t.Fatalf("cursor: %+v %v", cursor, err)
	}
}

// TestNumberedSteps proves parent isolation, empty/missing task handling and single-connection safety.
func TestNumberedSteps(t *testing.T) {
	h := setup(t)
	task := confirmed(t, h, "owner")
	other := preflight(t, h, "other")
	if err := h.db.Model(&d.Task{}).Where("id = ?", other.ID).Update("state", d.Fencing).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 25; index++ {
		planned(t, h, task.ID, fmt.Sprintf("step-%d", index))
	}
	planned(t, h, other.ID, "other-step")
	page, err := h.store.ListStepsPage(h.ctx, task.ID, c.PageQuery{Page: 3, PageSize: 10})
	if err != nil || page.Total != 25 || page.Page != 3 || len(page.Items) != 5 || page.Items[0].ID != 21 {
		t.Fatalf("steps jump: %+v %v", page, err)
	}
	page, err = h.store.ListStepsPage(h.ctx, other.ID, c.PageQuery{Page: 99, PageSize: 50})
	if err != nil || page.Total != 1 || page.Page != 1 || len(page.Items) != 1 || page.Items[0].TaskID != other.ID {
		t.Fatalf("scope: %+v %v", page, err)
	}
	empty := preflight(t, h, "empty")
	page, err = h.store.ListStepsPage(h.ctx, empty.ID, c.PageQuery{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("empty: %+v %v", page, err)
	}
	if _, err := h.store.ListStepsPage(h.ctx, 999, c.PageQuery{}); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := h.store.ListStepsPage(h.ctx, task.ID, c.PageQuery{PageSize: 101}); !errors.Is(err, c.ErrInvalid) {
		t.Fatal(err)
	}
	cursor, err := h.store.ListSteps(h.ctx, task.ID, 10, 10)
	if err != nil || cursor.NextAfterID != 20 || cursor.PageInfo != nil {
		t.Fatalf("cursor: %+v %v", cursor, err)
	}
}
