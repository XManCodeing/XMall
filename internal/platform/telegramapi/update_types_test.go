package telegramapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTopicServiceDTOEmptyObjectPresence(t *testing.T) {
	for _, test := range []struct {
		field            string
		closed, reopened bool
	}{
		{"forum_topic_closed", true, false}, {"forum_topic_reopened", false, true}, {"forum_topic_deleted", false, false},
	} {
		var message Message
		if err := json.Unmarshal([]byte(`{"`+test.field+`":{}}`), &message); err != nil {
			t.Fatal(err)
		}
		if (message.ForumTopicClosed != nil) != test.closed || (message.ForumTopicReopened != nil) != test.reopened {
			t.Fatal("empty-object presence lost", test.field)
		}
		payload, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var restored Message
		if err = json.Unmarshal(payload, &restored); err != nil || !reflect.DeepEqual(message, restored) {
			t.Fatal("service roundtrip", err)
		}
	}
}

func TestMemberDTOJSONRoundTrip(t *testing.T) {
	var update Update
	payload := `{"update_id":4,"my_chat_member":{"chat":{"id":-100,"type":"supergroup","is_forum":true},"from":{"id":1},"date":123,"old_chat_member":{"user":{"id":2},"status":"restricted","is_member":true,"until_date":456,"can_send_messages":false},"new_chat_member":{"user":{"id":2},"status":"administrator","is_anonymous":true,"can_manage_topics":true,"can_invite_users":false,"can_future_permission":true},"invite_link":{"invite_link":"test","creator":{"id":1},"creates_join_request":true,"is_primary":false,"is_revoked":false,"pending_join_request_count":3}}}`
	if err := json.Unmarshal([]byte(payload), &update); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	var restored Update
	if err = json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(update, restored) {
		t.Fatalf("roundtrip %s %v", data, err)
	}
	member := restored.MyChatMember
	if !member.NewChatMember.IsAnonymous || member.OldChatMember.IsAnonymous || member.From.ID == member.NewChatMember.User.ID || !member.Chat.IsForum || !member.NewChatMember.Permissions["can_manage_topics"] || !member.NewChatMember.Permissions["can_future_permission"] || member.OldChatMember.UntilDate != 456 {
		t.Fatalf("member %+v", member)
	}
	if denied, ok := member.NewChatMember.Permissions["can_invite_users"]; !ok || denied {
		t.Fatal("denial omitted")
	}
}
