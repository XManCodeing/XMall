package botapi

import (
	"context"
	"errors"

	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// Topics retains credentials within infrastructure. A request from admin HTTP
// without a live runtime lease context cannot execute platform side effects.
type Topics struct {
	Store  r.Store
	Cipher c.Cipher
	API    r.ForumTopicAPI
}

func (a *Topics) ExecuteTopic(ctx context.Context, route g.SupportRoute, action string, thread int64, title string) (c.TopicResult, error) {
	noEffect := c.TopicResult{State: "failed"}
	if a.Store == nil || a.Cipher == nil || a.API == nil || route.BotID == 0 || route.ChatID >= 0 || route.Generation == 0 || route.RouteVersion == 0 {
		return noEffect, c.ErrDenied
	}
	if action != "create" && action != "close" && action != "reopen" {
		return noEffect, c.ErrDenied
	}
	i, err := a.Store.Get(ctx, route.BotID)
	if err != nil {
		return noEffect, err
	}
	if i == nil || i.Generation != route.Generation || !i.Enabled || i.Mode != "internal" || i.State != "running" {
		return noEffect, c.ErrDenied
	}
	token, err := a.Cipher.Open(i.TokenCipher)
	if err != nil {
		return noEffect, c.ErrDenied
	}
	if err = r.CheckExecutionFence(ctx, route.BotID, route.Generation); err != nil {
		return noEffect, err
	}
	request := r.ForumTopicRequest{ChatID: route.ChatID, ThreadID: thread}
	result := c.TopicResult{State: "succeeded", ThreadID: thread}
	switch action {
	case "create":
		request.Name = title
		var created r.ForumTopicResult
		created, err = a.API.CreateForumTopic(ctx, token, request)
		result.ThreadID = created.ThreadID
	case "close":
		err = a.API.CloseForumTopic(ctx, token, request)
	case "reopen":
		err = a.API.ReopenForumTopic(ctx, token, request)
	}
	if err == nil && (action != "create" || result.ThreadID > 0) {
		return result, nil
	}
	result.State = "unknown"
	var platformError r.APIError
	if errors.As(err, &platformError) && !platformError.Uncertain() && platformError.StatusCode() >= 400 && platformError.StatusCode() < 500 {
		result.State = "failed"
		if platformError.StatusCode() == 429 {
			result.RetryAfterSeconds = int64(platformError.RetrySeconds())
			if result.RetryAfterSeconds < 1 {
				result.RetryAfterSeconds = 5
			}
		}
	}
	// Never return raw platform errors, which may include a URL with a token.
	return result, errors.New("support topic operation unresolved")
}
