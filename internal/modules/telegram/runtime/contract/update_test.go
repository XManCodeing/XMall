package contract

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestHistoricalJSONCompatibility(t *testing.T) {
	old := `{"id":8,"user_id":12,"chat_id":12,"private":true,"text":"hello","message_id":2,"message_kind":"text","locale":"zh","username":"name"}`
	var update Update
	if err := json.Unmarshal([]byte(old), &update); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	_ = json.Unmarshal([]byte(old), &before)
	_ = json.Unmarshal(data, &after)
	if !reflect.DeepEqual(before, after) || update.Type != "" || update.ChatType != "" || !update.Private {
		t.Fatalf("historical keys changed %s", data)
	}
	var message Message
	if err := json.Unmarshal([]byte(`{"chat_id":12,"text":"secret"}`), &message); err != nil {
		t.Fatal(err)
	}
	if message.TargetType.Normalize() != TargetShopPrivate {
		t.Fatal("legacy authorization changed")
	}
	for _, target := range []TargetType{"", TargetShopPrivate, TargetSupportPrivate, TargetSupportGroup, TargetMemberOperation} {
		if !target.Valid() {
			t.Fatalf("invalid %s", target)
		}
	}
	if TargetType("future").Normalize() != TargetType("future") || TargetType("future").Valid() {
		t.Fatal("unknown downgraded")
	}
	message.TargetType, message.BusinessRef, message.RouteVersion, message.ThreadID = TargetSupportGroup, "session:1", 9, 7
	data, _ = json.Marshal(message)
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(message, restored) {
		t.Fatalf("message roundtrip %s %v", data, err)
	}
}
