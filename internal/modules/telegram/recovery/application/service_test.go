package application

import (
	"context"
	"errors"
	"testing"
	"time"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	groupdomain "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// previewStore records the server-derived snapshot without providing any mutation executor.
type previewStore struct {
	c.Store
	input c.PreflightInput
	calls int
}

// CreatePreflight captures input and returns a synthetic pending task for orchestration tests.
func (store *previewStore) CreatePreflight(_ context.Context, input c.PreflightInput) (*d.Task, error) {
	store.calls++
	store.input = input
	return &d.Task{ID: 8, State: d.AwaitingConfirmation}, nil
}

// previewGroups returns a trusted fixed source and destination capability observation.
type previewGroups struct {
	preview groups.ReplacementPreview
	err     error
}

// PreviewReplacement returns the prepared read result or dependency failure without writes.
func (group previewGroups) PreviewReplacement(context.Context, groups.ReplacementInput) (*groups.ReplacementPreview, error) {
	return &group.preview, group.err
}

// TestPreviewGroupDerivesScope verifies the actor, source and CAS are not taken from arbitrary JSON.
func TestPreviewGroupDerivesScope(t *testing.T) {
	now := time.Now().UTC()
	store := &previewStore{}
	service := &Service{Store: store, Groups: previewGroups{preview: groups.ReplacementPreview{Source: groupdomain.Target{ID: 4, Version: 9, RouteVersion: 3, BotInstanceID: 2, TelegramChatID: -100}, Destination: groupdomain.Observation{Generation: 7}, CheckedAt: now, ExpiresAt: now.Add(time.Minute)}}}
	input := GroupPreflightInput{ReplacementInput: groups.ReplacementInput{TargetID: 4, ExpectedVersion: 9, ExpectedRouteVersion: 3, NewBotInstanceID: 2, NewTelegramChatID: "-200"}, RequestKey: "request-1", Reason: " replace group "}
	task, err := service.PreviewGroup(context.Background(), 6, input)
	if err != nil || task.State != d.AwaitingConfirmation {
		t.Fatalf("task=%+v error=%v", task, err)
	}
	got := store.input
	if got.ActorID != 6 || got.Reason != "replace group" || got.Snapshot.ExpectedOld.Config != 9 || got.Snapshot.Old.ChatID != -100 || got.Snapshot.New.ChatID != -200 || got.Snapshot.Scope.TargetIDs[0] != "4" {
		t.Fatalf("untrusted snapshot: %+v", got)
	}
	input.NewBotInstanceID = 3
	if _, err := service.PreviewGroup(context.Background(), 6, input); !errors.Is(err, c.ErrInvalid) || store.calls != 1 {
		t.Fatal("cross-Bot bypass accepted", err)
	}
}

// TestPreviewGroupValidationStopsPersistence verifies input and dependency failures never create tasks.
func TestPreviewGroupValidationStopsPersistence(t *testing.T) {
	store := &previewStore{}
	service := &Service{Store: store, Groups: previewGroups{err: groups.ErrCapability}}
	input := GroupPreflightInput{RequestKey: "key", Reason: "synthetic"}
	if _, err := service.PreviewGroup(context.Background(), 0, input); !errors.Is(err, c.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := service.PreviewGroup(context.Background(), 1, input); !errors.Is(err, groups.ErrCapability) {
		t.Fatal(err)
	}
	input.RequestKey = "https://invalid"
	if _, err := service.PreviewGroup(context.Background(), 1, input); !errors.Is(err, c.ErrInvalid) {
		t.Fatal(err)
	}
	if store.calls != 0 {
		t.Fatal("invalid preview persisted")
	}
}
