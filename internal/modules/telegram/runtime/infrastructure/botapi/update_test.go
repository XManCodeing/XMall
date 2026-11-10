package botapi

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	api "github.com/XManCodeing/XMall/internal/platform/telegramapi"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func decodeUpdate(t *testing.T, payload string) c.Update {
	t.Helper()
	var raw api.Update
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatal(err)
	}
	mapped := mapUpdate(raw)
	data, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	var restored c.Update
	if err = json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(mapped, restored) {
		t.Fatalf("persistence roundtrip: %v %s", err, data)
	}
	return mapped
}
func TestNativeTopicServiceProjection(t *testing.T) {
	for _, test := range []struct{ field, state string }{{"forum_topic_closed", "closed"}, {"forum_topic_reopened", "open"}} {
		update := decodeUpdate(t, `{"update_id":90,"message":{"message_id":8,"date":123,"chat":{"id":-100,"type":"supergroup"},"message_thread_id":42,"is_topic_message":true,"`+test.field+`":{},"text":"forged reply","reply_to_message":{"message_id":3,"chat":{"id":-100}}}}`)
		if update.TopicService == nil || update.TopicService.State != test.state || update.TopicService.Date != 123 || update.MessageKind != "service" || update.Reply != nil || update.Text != "" {
			t.Fatalf("service escaped projection: %+v", update)
		}
	}
	for _, field := range []string{"forum_topic_deleted", "new_chat_members"} {
		update := decodeUpdate(t, `{"update_id":91,"message":{"message_id":9,"chat":{"id":-100,"type":"supergroup"},"`+field+`":{},"reply_to_message":{"message_id":3}}}`)
		if update.TopicService != nil || update.Reply != nil || update.MessageKind != "service" {
			t.Fatalf("invented event or reply: %+v", update)
		}
	}
	update := decodeUpdate(t, `{"update_id":92,"edited_message":{"forum_topic_closed":{},"chat":{"id":-100}}}`)
	if update.TopicService != nil || update.MessageKind != "service" {
		t.Fatal("edited snapshot became fresh event")
	}
}

// TestActorNames uses real from only, never chat/reply/forward identity.
func TestActorNames(t *testing.T) {
	update := decodeUpdate(t, `{"update_id":1,"message":{"message_id":2,"from":{"id":42,"first_name":"真实","last_name":"姓名","username":"actor"},"chat":{"id":42,"type":"private","first_name":"Chat"},"text":"hello","forward_origin":{"type":"user","sender_user":{"id":9,"first_name":"Forward"}}}}`)
	if update.Actor == nil || update.Actor.ID != 42 || update.Actor.Username != "actor" || update.FirstName != "真实" || update.LastName != "姓名" || update.Username != "actor" {
		t.Fatalf("actor names lost: %+v", update)
	}
	callback := decodeUpdate(t, `{"update_id":2,"callback_query":{"id":"q","from":{"id":42,"first_name":"Click","last_name":"Actor"},"message":{"message_id":2,"from":{"id":9,"first_name":"Bot"},"chat":{"id":42,"type":"private"}}}}`)
	if callback.Actor == nil || callback.Actor.ID != 42 || callback.Actor.Username != "" || callback.FirstName != "Click" || callback.LastName != "Actor" {
		t.Fatal("callback used panel sender")
	}
	anonymous := decodeUpdate(t, `{"update_id":3,"message":{"message_id":3,"from":{"id":9,"first_name":"Fake"},"sender_chat":{"id":-10},"chat":{"id":-10,"type":"supergroup"}}}`)
	if anonymous.Actor != nil || anonymous.FirstName != "" || anonymous.LastName != "" || anonymous.UserID != 0 {
		t.Fatal("sender_chat exposed placeholder identity")
	}
}

func TestRichMessageMapping(t *testing.T) {
	u := decodeUpdate(t, `{"update_id":55,"message":{"message_id":4,"message_thread_id":7,"is_topic_message":true,"from":{"id":99},"sender_chat":{"id":-12,"type":"supergroup","is_forum":true},"chat":{"id":-12,"type":"supergroup"},"caption":"hello","caption_entities":[{"type":"text_mention","offset":2,"length":3,"user":{"id":9}}],"entities":[{"type":"custom_emoji","offset":0,"length":2,"custom_emoji_id":"emoji"}],"media_group_id":"album","photo":[{"file_id":"small","file_unique_id":"unique","width":10,"height":20,"file_size":50},{"file_id":"large","width":100,"height":200}],"reply_to_message":{"message_id":3,"message_thread_id":7,"chat":{"id":-12,"type":"supergroup"},"from":{"id":11}}}}`)
	if u.Type != "message" || u.Private || u.ChatID != -12 || u.UserID != 0 || u.SenderChat.ID != -12 || !u.SenderChat.IsForum || u.ThreadID != 7 || !u.IsTopicMessage {
		t.Fatalf("context %+v", u)
	}
	if u.Reply.ChatID != -12 || u.Reply.MessageID != 3 || u.Reply.ThreadID != 7 || u.Reply.UserID != 11 {
		t.Fatalf("reply %+v", u.Reply)
	}
	if u.Caption != "hello" || u.MediaGroupID != "album" || len(u.Media) != 2 || u.Media[0].FileUniqueID != "unique" || u.CaptionEntities[0].User.ID != 9 || u.Entities[0].Length != 2 {
		t.Fatalf("content %+v", u)
	}
}
func TestMemberAndJoinMapping(t *testing.T) {
	for _, kind := range []string{"chat_member", "my_chat_member"} {
		u := decodeUpdate(t, `{"update_id":8,"`+kind+`":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":20},"date":123,"old_chat_member":{"status":"left","user":{"id":30}},"new_chat_member":{"status":"administrator","is_anonymous":true,"user":{"id":30},"can_invite_users":true,"can_restrict_members":false},"invite_link":{"invite_link":"test-link","creator":{"id":40},"creates_join_request":true},"via_join_request":true,"via_chat_folder_invite_link":true}}`)
		if !u.Member.New.IsAnonymous || u.Member.Old.IsAnonymous || u.Type != kind || u.UserID != 20 || u.ChatID != -100 || u.Private || u.Member.Operator.ID != 20 || u.Member.Subject.ID != 30 || u.Member.New.User.ID != 30 || u.Member.Old.Status != "left" || !u.Member.New.Permissions["can_invite_users"] || !u.Member.ViaJoinRequest || !u.Member.ViaChatFolderInviteLink {
			t.Fatalf("member %+v", u)
		}
		if value, ok := u.Member.New.Permissions["can_restrict_members"]; !ok || value {
			t.Fatal("explicit denial lost")
		}
	}
	u := decodeUpdate(t, `{"update_id":9,"chat_join_request":{"chat":{"id":-100,"type":"supergroup"},"from":{"id":30},"user_chat_id":900719925474099,"date":123,"bio":"bio","invite_link":{"invite_link":"test-link","creator":{"id":40},"creates_join_request":true}}}`)
	if u.JoinRequest.UserChatID != 900719925474099 || u.ChatID != -100 || u.UserID != 30 || u.Private || u.JoinRequest.InviteLink.Creator.ID != 40 {
		t.Fatalf("join %+v", u)
	}
}
func TestUpdateKindsAndUnknownPreservation(t *testing.T) {
	for _, kind := range []string{"message", "edited_message", "channel_post", "edited_channel_post"} {
		u := decodeUpdate(t, `{"update_id":17,"`+kind+`":{"message_id":9,"chat":{"id":-22,"type":"channel"},"text":"x"}}`)
		if u.Type != kind || u.Text != "x" || u.UserID != 0 || u.Private {
			t.Fatalf("%+v", u)
		}
	}
	for _, payload := range []string{`{"update_id":20,"poll":{"id":"unknown"}}`, `{"update_id":20}`} {
		u := decodeUpdate(t, payload)
		if u.ID != 20 || u.Type != "unknown" || u.ChatID != 0 || u.UserID != 0 || u.Private {
			t.Fatalf("%+v", u)
		}
	}
	u := decodeUpdate(t, `{"update_id":21,"callback_query":{"id":"q","from":{"id":11},"inline_message_id":"inline","data":"s:x"}}`)
	if u.Type != "callback_query" || u.CallbackID != "q" || u.UserID != 11 || u.ChatID != 0 || u.Private {
		t.Fatalf("inline callback %+v", u)
	}
	for _, payload := range []string{`{"update_id":22,"message":{"chat":{"id":-1,"type":"group"},"migrate_to_chat_id":-100}}`, `{"update_id":22,"message":{"chat":{"id":-100,"type":"supergroup"},"migrate_from_chat_id":-1}}`} {
		u = decodeUpdate(t, payload)
		if u.Migration.FromChatID != -1 || u.Migration.ToChatID != -100 || u.MessageKind != "migration" {
			t.Fatalf("migration %+v", u)
		}
	}
}

// TestPrivateUnknownContentRemainsRejectable prevents private unsupported messages disappearing as topic service events.
func TestPrivateUnknownContentRemainsRejectable(t *testing.T) {
	update := decodeUpdate(t, `{"update_id":93,"message":{"message_id":10,"chat":{"id":42,"type":"private"},"from":{"id":42},"poll":{"id":"synthetic"}}}`)
	if update.MessageKind != "unknown" || update.TopicService != nil || !update.Private {
		t.Fatal("private rejection lost", update.MessageKind)
	}
}

func TestAllMediaKinds(t *testing.T) {
	for _, kind := range []string{"video", "voice", "audio", "animation", "video_note", "sticker", "document"} {
		t.Run(kind, func(t *testing.T) {
			u := decodeUpdate(t, `{"update_id":1,"message":{"chat":{"id":1,"type":"private"},"`+kind+`":{"file_id":"file","file_unique_id":"unique","file_size":42,"width":12,"height":13,"length":14,"duration":15,"file_name":"name","mime_type":"mime"}}}`)
			if u.MessageKind != kind || len(u.Media) != 1 || u.Media[0].Type != kind || u.Media[0].FileID != "file" || u.Media[0].FileUniqueID != "unique" || u.Media[0].FileSize != 42 {
				t.Fatalf("media %+v", u)
			}
		})
	}
}
func TestExplicitSubscription(t *testing.T) {
	calls := 0
	want := []string{"message", "callback_query"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req api.GetUpdatesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Offset != 123 || req.AllowedUpdates == nil || !reflect.DeepEqual(*req.AllowedUpdates, want) {
			t.Errorf("request %+v", req)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":124}]}`))
	}))
	defer server.Close()
	a := Adapter{Options: []api.Option{api.WithBaseURL(server.URL)}}
	if _, err := a.Poll(context.Background(), "dummy", 123); err != nil {
		t.Fatal(err)
	}
	want = []string{"chat_member", "my_chat_member", "chat_join_request", "edited_message"}
	updates, err := a.PollWithSubscription(context.Background(), "dummy", 123, want)
	if err != nil || len(updates) != 1 || updates[0].ID != 124 {
		t.Fatalf("%+v %v", updates, err)
	}
	for _, invalid := range [][]string{nil, {}, {""}, {" "}, {"message "}, {"unknown"}, {"message", "message"}} {
		if _, err := a.PollWithSubscription(context.Background(), "dummy", 123, invalid); !errors.Is(err, c.ErrInvalidSubscription) {
			t.Fatalf("accepted %v: %v", invalid, err)
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected calls %d", calls)
	}
}
func TestUnsupportedTargetsNeverSendText(t *testing.T) {
	for _, m := range []c.Message{{TargetType: "future"}, {TargetType: c.TargetSupportGroup}, {TargetType: c.TargetMemberOperation}, {TargetType: c.TargetSupportPrivate}, {ThreadID: 3}, {TargetKind: "video"}} {
		_, err := (Adapter{}).Apply(context.Background(), "dummy", m)
		var apiErr c.APIError
		if !errors.As(err, &apiErr) || apiErr.Uncertain() || apiErr.StatusCode() != 400 {
			t.Fatalf("unsafe fallback %+v %v", m, err)
		}
	}
}
