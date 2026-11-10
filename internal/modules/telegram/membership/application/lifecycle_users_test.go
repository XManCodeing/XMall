package application_test

import (
	"context"
	"errors"
	"testing"

	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// lifecycleUserStoreStub adds only the optional reader to the existing repository boundary.
type lifecycleUserStoreStub struct {
	c.LifecycleRepository
	calls int
	err   error
}

// LifecycleUsersPage verifies service forwarding and returns a fixed page or configured error.
func (stub *lifecycleUserStoreStub) LifecycleUsersPage(_ context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleUserTasks, int64, error) {
	stub.calls++
	if bot != 9 || page != 2 || size != 10 || len(status) != 1 || status[0] != "processing" {
		return nil, 0, c.ErrInvalid
	}
	return []c.LifecycleUserTasks{{TelegramID: 123, Tasks: []c.LifecycleTask{{ID: 1}}}}, 11, stub.err
}

// lifecycleSearchStoreStub captures search while preserving the legacy optional reader.
type lifecycleSearchStoreStub struct {
	lifecycleUserStoreStub
	keyword string
}

// SearchLifecycleUsersPage captures the normalized keyword and reuses scope assertions.
// Returns the same user envelope or configured repository error.
func (stub *lifecycleSearchStoreStub) SearchLifecycleUsersPage(ctx context.Context, bot uint, page, size int, status, keyword string) ([]c.LifecycleUserTasks, int64, error) {
	stub.keyword = keyword
	return stub.LifecycleUsersPage(ctx, bot, page, size, status)
}

// TestLifecycleSearchService checks normalization, missing capabilities and error propagation.
func TestLifecycleSearchService(t *testing.T) {
	ctx := context.Background()
	store := &lifecycleSearchStoreStub{}
	service := &a.Lifecycle{Store: store}
	rows, total, err := service.SearchLifecycleUsersPage(ctx, 9, 2, 10, "processing", " @SEVEN_Time ")
	if err != nil || len(rows) != 1 || total != 11 || store.keyword != "@seven_time" {
		t.Fatalf("search forwarding: %+v %d %v", rows, total, err)
	}
	if _, _, err := service.SearchLifecycleUsersPage(ctx, 9, 2, 10, "processing", "%"); !errors.Is(err, c.ErrInvalid) || store.calls != 1 {
		t.Fatalf("invalid keyword reached repository: %v", err)
	}
	store.err = errors.New("search unavailable")
	if _, _, err := service.SearchLifecycleUsersPage(ctx, 9, 2, 10, "processing", "alice"); !errors.Is(err, store.err) {
		t.Fatalf("lost error: %v", err)
	}
	legacy := &lifecycleUserStoreStub{}
	service.Store = legacy
	if _, _, err := service.SearchLifecycleUsersPage(ctx, 9, 2, 10, "processing", ""); err != nil || legacy.calls != 1 {
		t.Fatalf("empty search compatibility: %v", err)
	}
	if _, _, err := service.SearchLifecycleUsersPage(ctx, 9, 2, 10, "processing", "alice"); !errors.Is(err, c.ErrCapability) || legacy.calls != 1 {
		t.Fatalf("search silently ignored: %v", err)
	}
}

// TestLifecycleUsersService validates untrusted inputs before delegation and preserves store failures.
func TestLifecycleUsersService(t *testing.T) {
	ctx := context.Background()
	store := &lifecycleUserStoreStub{}
	service := &a.Lifecycle{Store: store}
	groups, total, err := service.LifecycleUsersPage(ctx, 9, 2, 10, "processing")
	if err != nil || total != 11 || len(groups) != 1 || groups[0].TelegramID != 123 || store.calls != 1 {
		t.Fatalf("incorrect forwarding: %+v %d %v", groups, total, err)
	}
	for _, input := range [][3]int{{0, 2, 10}, {9, 0, 10}, {9, 2, 101}} {
		if _, _, err := service.LifecycleUsersPage(ctx, uint(input[0]), input[1], input[2], "processing"); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid page: %v", err)
		}
	}
	for _, status := range [][]string{{"unknown"}, {"processing", "completed"}} {
		if _, _, err := service.LifecycleUsersPage(ctx, 9, 2, 10, status...); !errors.Is(err, c.ErrInvalid) {
			t.Fatalf("accepted invalid filter: %v", err)
		}
	}
	if store.calls != 1 {
		t.Fatal("invalid parameters reached store")
	}
	store.err = errors.New("reader unavailable")
	if _, _, err := service.LifecycleUsersPage(ctx, 9, 2, 10, "processing"); !errors.Is(err, store.err) {
		t.Fatalf("lost store failure: %v", err)
	}
	service.Store = nil
	if _, _, err := service.LifecycleUsersPage(ctx, 9, 2, 10); !errors.Is(err, c.ErrCapability) {
		t.Fatalf("missing capability did not fail closed: %v", err)
	}
}
