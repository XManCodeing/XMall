package productmedia

import (
	"context"
	"errors"
	"reflect"
	"testing"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

type transferFake struct {
	rt.API
	calls   int
	request rt.TransferRequest
	err     error
}

// Transfer records calls and returns partial observations with the supplied error.
func (fake *transferFake) Transfer(_ context.Context, _ string, request rt.TransferRequest) (rt.TransferResult, error) {
	fake.calls++
	fake.request = request
	return rt.TransferResult{Messages: []rt.DeliveryResult{{MessageID: 7}}}, fake.err
}

func TestTransferWrapperDoesNotFallbackRetryOrTouchMedia(t *testing.T) {
	failure := errors.New("uncertain")
	fake := &transferFake{err: failure}
	wrapper := New(fake, "/must-not-open", nil)
	request := rt.TransferRequest{TargetType: rt.TargetSupportGroup, BusinessRef: "session", RouteVersion: 3, ChatID: -10, ThreadID: 9, ProtectContent: true, Contents: []rt.TransferContent{{Kind: "photo", FileID: "photo"}, {Kind: "video", FileID: "video"}}}
	result, err := wrapper.Transfer(context.Background(), "test", request)
	if !errors.Is(err, failure) || fake.calls != 1 || !reflect.DeepEqual(request, fake.request) || len(result.Messages) != 1 || result.Messages[0].MessageID != 7 {
		t.Fatal(result, err, fake.calls)
	}
}

func TestTransferAndTopicsUnsupportedKeepLegacyAPICompatible(t *testing.T) {
	// Embedding only the old API proves no new methods were added to that interface.
	var legacy rt.API = &subscriptionFake{}
	wrapper := New(legacy, "", nil)
	if _, err := wrapper.Transfer(context.Background(), "test", rt.TransferRequest{}); !errors.Is(err, rt.ErrTransferUnsupported) {
		t.Fatal(err)
	}
	if _, err := wrapper.CreateForumTopic(context.Background(), "test", rt.ForumTopicRequest{}); !errors.Is(err, rt.ErrForumTopicUnsupported) {
		t.Fatal(err)
	}
	if err := wrapper.ReopenForumTopic(context.Background(), "test", rt.ForumTopicRequest{}); !errors.Is(err, rt.ErrForumTopicUnsupported) {
		t.Fatal(err)
	}
	if err := wrapper.CloseForumTopic(context.Background(), "test", rt.ForumTopicRequest{}); !errors.Is(err, rt.ErrForumTopicUnsupported) {
		t.Fatal(err)
	}
}
