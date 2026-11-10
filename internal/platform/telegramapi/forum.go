package telegramapi

import (
	"context"
	"strings"
)

// ForumTopicRequest is restricted to forum supergroups; create requires a name
// and zero thread, whereas close/reopen require a positive thread and no name.
type ForumTopicRequest struct {
	ChatID   int64  `json:"chat_id"`
	ThreadID int64  `json:"message_thread_id,omitempty"`
	Name     string `json:"name,omitempty"`
}

type ForumTopic struct {
	ThreadID int64 `json:"message_thread_id"`
}

// CreateForumTopic performs exactly one create; absent IDs are uncertain results.
// Callers must persist a claim before this call and reconcile unknown outcomes.
func (client *Client) CreateForumTopic(ctx context.Context, request ForumTopicRequest) (ForumTopic, error) {
	if request.ChatID >= 0 || request.ThreadID != 0 || strings.TrimSpace(request.Name) == "" {
		return ForumTopic{}, invalid("invalid_content")
	}
	if err := validateContent(request.Name, "", 128, nil); err != nil {
		return ForumTopic{}, err
	}
	var result ForumTopic
	if err := client.callJSON(ctx, "createForumTopic", request, &result); err != nil {
		return result, err
	}
	if result.ThreadID <= 0 {
		return result, &UncertainResultError{}
	}
	return result, nil
}

// ReopenForumTopic reopens one existing topic without creating a replacement.
func (client *Client) ReopenForumTopic(ctx context.Context, request ForumTopicRequest) error {
	return client.changeForumTopic(ctx, "reopenForumTopic", request)
}

// CloseForumTopic closes one topic; it does not delete topic/message mappings.
func (client *Client) CloseForumTopic(ctx context.Context, request ForumTopicRequest) error {
	return client.changeForumTopic(ctx, "closeForumTopic", request)
}

// changeForumTopic validates the destination and treats false success as unknown.
func (client *Client) changeForumTopic(ctx context.Context, method string, request ForumTopicRequest) error {
	if request.ChatID >= 0 || request.ThreadID <= 0 || request.Name != "" {
		return invalid("invalid_content")
	}
	var result bool
	if err := client.callJSON(ctx, method, request, &result); err != nil {
		return err
	}
	if !result {
		return &UncertainResultError{}
	}
	return nil
}
