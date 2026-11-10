package application

import (
	"context"
	"strings"
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

type operationMock struct {
	row     d.OperationReference
	applied int
}

func (mock *operationMock) PrepareOperation(_ context.Context, bot uint, update r.Update, version, generation uint64, action, note, ref string, now time.Time) (d.OperationReference, error) {
	mock.row = d.OperationReference{Ref: ref, BotID: bot, ActorID: update.UserID, ChatID: update.ChatID, ThreadID: update.ThreadID, RouteVersion: version, Generation: generation, Action: action, NoteCipher: note, ExpiresAt: now.Add(time.Minute)}
	return mock.row, nil
}
func (mock *operationMock) Operation(context.Context, uint, string) (d.OperationReference, error) {
	return mock.row, nil
}
func (mock *operationMock) ApplyOperation(context.Context, uint, r.Update, string, time.Time) error {
	mock.applied++
	return nil
}

// verificationOperationMock isolates automatic cards while retaining the manual mock behavior.
type verificationOperationMock struct{ operationMock }

// VerificationSessions returns no scheduled work for callback-only tests.
func (*verificationOperationMock) VerificationSessions(context.Context, uint, uint, int, time.Time) ([]d.Session, error) {
	return nil, nil
}

// TopicIdentity returns an unused identity for callback-only tests.
func (*verificationOperationMock) TopicIdentity(context.Context, uint, uint) (d.Session, error) {
	return d.Session{}, nil
}

// EnsureVerificationControl returns no scheduled control for callback-only tests.
func (*verificationOperationMock) EnsureVerificationControl(context.Context, uint, uint, uint64, time.Time) (*d.OperationControl, error) {
	return nil, nil
}

// ApplyVerificationOperation counts calls after live membership authorization.
func (mock *verificationOperationMock) ApplyVerificationOperation(context.Context, uint, r.Update, string, uint64, time.Time) error {
	mock.applied++
	return nil
}

// TestVerificationCardLiveMembers permits different real members but rejects removed and anonymous users.
func TestVerificationCardLiveMembers(t *testing.T) {
	now := time.Now().UTC()
	mock := &verificationOperationMock{operationMock: operationMock{row: d.OperationReference{Ref: strings.Repeat("x", 24), ControlRef: strings.Repeat("y", 24), ChatID: -100, ThreadID: 20, RouteVersion: 1, Generation: 99, Action: c.OperationBlock, ExpiresAt: now.Add(time.Hour)}}}
	members := &liveMembers{}
	service := OperationService{Store: mock, Members: members, Now: func() time.Time { return now }}
	ctx := r.WithExecutionFence(context.Background(), &replyFence{})
	update := r.Update{UserID: 55, ChatID: -100, ThreadID: 20, MessageID: 123, IsTopicMessage: true, CallbackID: "click", CallbackData: c.OperationCallbackPrefix + mock.row.Ref}
	if err := service.execute(ctx, 1, update); err != nil {
		t.Fatal(err)
	}
	update.UserID = 56
	if err := service.execute(ctx, 1, update); err != nil || mock.applied != 2 {
		t.Fatal("real member denied", err)
	}
	members.removed = true
	if err := service.execute(ctx, 1, update); err != c.ErrDenied || mock.applied != 2 {
		t.Fatal("removed member accepted", err)
	}
	members.removed = false
	update.UserID = 0
	if err := service.execute(ctx, 1, update); err != c.ErrDenied {
		t.Fatal("anonymous actor accepted", err)
	}
	update.UserID = 55
	mock.row.RouteVersion++
	if err := service.execute(ctx, 1, update); err != c.ErrDenied {
		t.Fatal("old route accepted", err)
	}
}

func TestOperationsLiveMembershipEveryClick(t *testing.T) {
	mock := &operationMock{}
	members := &liveMembers{}
	now := time.Now().UTC()
	service := OperationService{Store: mock, Members: members, Now: func() time.Time { return now }}
	ctx := r.WithExecutionFence(context.Background(), &replyFence{})
	update := r.Update{UserID: 55, ChatID: -100, ThreadID: 20, IsTopicMessage: true, CallbackID: "click"}
	data, err := service.Prepare(ctx, 1, c.OperationIntent{Update: update, Action: c.OperationBlock})
	if err != nil || !strings.HasPrefix(data, c.OperationCallbackPrefix) || len(data) > 64 {
		t.Fatal(data, err)
	}
	update.CallbackData = data
	if err = service.execute(ctx, 1, update); err != nil || mock.applied != 1 {
		t.Fatal(err, mock.applied)
	}
	members.removed = true
	if err = service.execute(ctx, 1, update); err != c.ErrDenied || mock.applied != 1 {
		t.Fatal("departed member", err, mock.applied)
	}
	members.removed = false
	update.UserID = 56
	if err = service.execute(ctx, 1, update); err != c.ErrDenied {
		t.Fatal("forwarded callback", err)
	}
	update.UserID = 55
	mock.row.RouteVersion++
	if err = service.execute(ctx, 1, update); err != c.ErrDenied {
		t.Fatal("old route", err)
	}
}
