package productmedia

import (
	"context"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
)

var _ rt.TransferAPI = (*API)(nil)
var _ rt.ForumTopicAPI = (*API)(nil)

// Transfer bypasses product-photo fallback/cache entirely. Unsupported adapters
// fail closed: no legacy Send/Apply conversion and no retries on any error.
func (wrapper *API) Transfer(ctx context.Context, token string, request rt.TransferRequest) (rt.TransferResult, error) {
	transport, ok := wrapper.API.(rt.TransferAPI)
	if !ok {
		return rt.TransferResult{}, rt.ErrTransferUnsupported
	}
	return transport.Transfer(ctx, token, request)
}

// CreateForumTopic forwards one creation without fallback or retries.
func (wrapper *API) CreateForumTopic(ctx context.Context, token string, request rt.ForumTopicRequest) (rt.ForumTopicResult, error) {
	transport, ok := wrapper.API.(rt.ForumTopicAPI)
	if !ok {
		return rt.ForumTopicResult{}, rt.ErrForumTopicUnsupported
	}
	return transport.CreateForumTopic(ctx, token, request)
}

// ReopenForumTopic forwards an explicit existing topic only.
func (wrapper *API) ReopenForumTopic(ctx context.Context, token string, request rt.ForumTopicRequest) error {
	transport, ok := wrapper.API.(rt.ForumTopicAPI)
	if !ok {
		return rt.ErrForumTopicUnsupported
	}
	return transport.ReopenForumTopic(ctx, token, request)
}

// CloseForumTopic forwards closure; it never deletes or recreates a topic.
func (wrapper *API) CloseForumTopic(ctx context.Context, token string, request rt.ForumTopicRequest) error {
	transport, ok := wrapper.API.(rt.ForumTopicAPI)
	if !ok {
		return rt.ErrForumTopicUnsupported
	}
	return transport.CloseForumTopic(ctx, token, request)
}
