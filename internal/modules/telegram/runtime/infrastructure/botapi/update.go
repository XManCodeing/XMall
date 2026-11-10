package botapi

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"maps"
)

var _ c.SubscriptionAPI = Adapter{}

func (a Adapter) PollWithSubscription(ctx context.Context, token string, offset int64, allowed []string) ([]c.Update, error) {
	if err := c.ValidateSubscription(allowed); err != nil {
		return nil, err
	}
	allowed = append([]string(nil), allowed...)
	client, err := a.client(token)
	if err != nil {
		return nil, err
	}
	updates, err := client.GetUpdates(ctx, api.GetUpdatesRequest{Offset: offset, Limit: 100, Timeout: 25, AllowedUpdates: &allowed})
	if err != nil {
		return nil, err
	}
	result := make([]c.Update, 0, len(updates))
	for _, raw := range updates {
		result = append(result, mapUpdate(raw))
	}
	return result, nil
}

func userRef(u api.User) c.UserReference {
	return c.UserReference{ID: u.ID, IsBot: u.IsBot, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, LanguageCode: u.LanguageCode}
}
func chatRef(chat *api.Chat) *c.ChatReference {
	if chat == nil {
		return nil
	}
	return &c.ChatReference{ID: chat.ID, Type: chat.Type, Title: chat.Title, Username: chat.Username, IsForum: chat.IsForum}
}
func entities(in []api.MessageEntity) []c.MessageEntity {
	if in == nil {
		return nil
	}
	out := make([]c.MessageEntity, 0, len(in))
	for _, e := range in {
		item := c.MessageEntity{Type: e.Type, Offset: e.Offset, Length: e.Length, URL: e.URL, Language: e.Language, CustomEmojiID: e.CustomEmojiID}
		if e.User != nil {
			user := userRef(*e.User)
			item.User = &user
		}
		out = append(out, item)
	}
	return out
}
func inviteRef(in *api.ChatInviteLink) *c.InviteLink {
	if in == nil {
		return nil
	}
	return &c.InviteLink{InviteLink: in.InviteLink, Creator: userRef(in.Creator), CreatesJoinRequest: in.CreatesJoinRequest, IsPrimary: in.IsPrimary, IsRevoked: in.IsRevoked, Name: in.Name, ExpireDate: in.ExpireDate, MemberLimit: in.MemberLimit, PendingJoinRequestCount: in.PendingJoinRequestCount}
}
func memberRef(in api.ChatMember) c.ChatMember {
	return c.ChatMember{User: userRef(in.User), Status: in.Status, UntilDate: in.UntilDate, IsMember: in.IsMember, IsAnonymous: in.IsAnonymous, CustomTitle: in.CustomTitle, Permissions: maps.Clone(in.Permissions)}
}

func mapUpdate(raw api.Update) c.Update {
	u := c.Update{ID: raw.UpdateID, Type: "unknown"}
	var message *api.Message
	var from *api.User
	var chat *api.Chat
	var member *api.ChatMemberUpdated
	switch {
	case raw.Message != nil:
		u.Type, message = "message", raw.Message
	case raw.EditedMessage != nil:
		u.Type, message = "edited_message", raw.EditedMessage
	case raw.ChannelPost != nil:
		u.Type, message = "channel_post", raw.ChannelPost
	case raw.EditedChannelPost != nil:
		u.Type, message = "edited_channel_post", raw.EditedChannelPost
	case raw.CallbackQuery != nil:
		q := raw.CallbackQuery
		u.Type, message, from = "callback_query", q.Message, &q.From
		u.CallbackID, u.CallbackData = q.ID, q.Data
	case raw.MyChatMember != nil:
		u.Type, member = "my_chat_member", raw.MyChatMember
	case raw.ChatMember != nil:
		u.Type, member = "chat_member", raw.ChatMember
	case raw.ChatJoinRequest != nil:
		q := raw.ChatJoinRequest
		u.Type, from, chat = "chat_join_request", &q.From, &q.Chat
		u.JoinRequest = &c.JoinRequest{User: userRef(q.From), UserChatID: q.UserChatID, Date: q.Date, Bio: q.Bio, InviteLink: inviteRef(q.InviteLink)}
	}
	if message != nil {
		chat = &message.Chat
		if from == nil && message.SenderChat == nil {
			from = message.From
		}
		u.MessageID, u.MessageKind = message.MessageID, messageKind(*message)
		// Callback panel text is not new user input; retain legacy callback behavior.
		if u.Type != "callback_query" {
			u.Text = message.Text
		}
		u.HasProtectedContent = message.HasProtectedContent
		u.ThreadID, u.IsTopicMessage = message.MessageThreadID, message.IsTopicMessage
		u.SenderChat = chatRef(message.SenderChat)
		u.Caption, u.Entities, u.CaptionEntities = message.Caption, entities(message.Entities), entities(message.CaptionEntities)
		u.MediaGroupID, u.Media = message.MediaGroupID, mediaRefs(*message)
		if reply := message.ReplyToMessage; reply != nil {
			u.Reply = &c.ReplyReference{ChatID: reply.Chat.ID, MessageID: reply.MessageID, ThreadID: reply.MessageThreadID, SenderChat: chatRef(reply.SenderChat)}
			if reply.From != nil && reply.SenderChat == nil {
				u.Reply.UserID = reply.From.ID
			}
		}
		if message.MigrateToChatID != 0 {
			u.Migration = &c.ChatMigration{FromChatID: message.Chat.ID, ToChatID: message.MigrateToChatID}
		}
		if message.MigrateFromChatID != 0 {
			u.Migration = &c.ChatMigration{FromChatID: message.MigrateFromChatID, ToChatID: message.Chat.ID}
		}
		projectTopicService(&u, *message)
	}
	if member != nil {
		chat, from = &member.Chat, &member.From
		u.Member = &c.MemberUpdate{Operator: userRef(member.From), Subject: userRef(member.NewChatMember.User), Date: member.Date, Old: memberRef(member.OldChatMember), New: memberRef(member.NewChatMember), InviteLink: inviteRef(member.InviteLink), ViaJoinRequest: member.ViaJoinRequest, ViaChatFolderInviteLink: member.ViaChatFolderInviteLink}
	}
	if chat != nil {
		u.ChatID, u.ChatType, u.Private = chat.ID, chat.Type, chat.Type == "private"
	}
	if from != nil {
		actor := userRef(*from)
		u.Actor = &actor
		u.UserID, u.Locale, u.Username = from.ID, from.LanguageCode, from.Username
		u.FirstName, u.LastName = from.FirstName, from.LastName
	}
	return u
}

// projectTopicService projects native service fields and strips reply/content
// paths. Callback/edited snapshots cannot be replayed as fresh topic events.
func projectTopicService(update *c.Update, message api.Message) {
	if message.ForumTopicClosed == nil && message.ForumTopicReopened == nil {
		// Private unknown payloads must remain visible to support's safe rejection
		// feedback. Only group unknowns use the conservative service-event sink.
		if update.Type == "message" && update.MessageKind == "unknown" && message.Chat.Type != "private" {
			update.MessageKind, update.Reply = "service", nil
		}
		return
	}
	update.MessageKind = "service"
	update.Text, update.Caption, update.CallbackData = "", "", ""
	update.Entities, update.CaptionEntities, update.Media = nil, nil, nil
	update.Reply, update.MediaGroupID = nil, ""
	if update.Type != "message" || (message.ForumTopicClosed != nil && message.ForumTopicReopened != nil) {
		return
	}
	state := "closed"
	if message.ForumTopicReopened != nil {
		state = "open"
	}
	update.TopicService = &c.TopicServiceEvent{State: state, Date: message.Date}
}

func mediaRefs(m api.Message) []c.Media {
	var out []c.Media
	for _, p := range m.Photo {
		out = append(out, c.Media{Type: "photo", FileID: p.FileID, FileUniqueID: p.FileUniqueID, FileSize: p.FileSize, Width: p.Width, Height: p.Height})
	}
	if d := m.Document; d != nil {
		out = append(out, c.Media{Type: "document", FileID: d.FileID, FileUniqueID: d.FileUniqueID, FileSize: d.FileSize, FileName: d.FileName, MimeType: d.MimeType})
	}
	for _, item := range []struct {
		kind  string
		video *api.Video
	}{{"video", m.Video}, {"animation", m.Animation}} {
		if v := item.video; v != nil {
			out = append(out, c.Media{Type: item.kind, FileID: v.FileID, FileUniqueID: v.FileUniqueID, FileSize: v.FileSize, Width: v.Width, Height: v.Height, Duration: v.Duration, FileName: v.FileName, MimeType: v.MimeType})
		}
	}
	if v := m.Voice; v != nil {
		out = append(out, c.Media{Type: "voice", FileID: v.FileID, FileUniqueID: v.FileUniqueID, FileSize: v.FileSize, Duration: v.Duration, MimeType: v.MimeType})
	}
	if a := m.Audio; a != nil {
		out = append(out, c.Media{Type: "audio", FileID: a.FileID, FileUniqueID: a.FileUniqueID, FileSize: a.FileSize, Duration: a.Duration, FileName: a.FileName, MimeType: a.MimeType})
	}
	if v := m.VideoNote; v != nil {
		out = append(out, c.Media{Type: "video_note", FileID: v.FileID, FileUniqueID: v.FileUniqueID, FileSize: v.FileSize, Duration: v.Duration, Width: v.Length, Height: v.Length})
	}
	if s := m.Sticker; s != nil {
		out = append(out, c.Media{Type: "sticker", FileID: s.FileID, FileUniqueID: s.FileUniqueID, FileSize: s.FileSize, Width: s.Width, Height: s.Height})
	}
	return out
}
