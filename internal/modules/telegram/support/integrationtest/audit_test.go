package integrationtest

import (
	"testing"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// TestUnregisteredTopicRepliesRemainIsolated forwards only explicitly same-topic references.
func TestUnregisteredTopicRepliesRemainIsolated(t *testing.T) {
	fixture, _, ctx := topicReplyFixture(t)
	for index, thread := range []int64{20, 21} {
		source := topicStaffMessage(int64(500+index), thread)
		source.Reply = &r.ReplyReference{ChatID: -100, ThreadID: thread, MessageID: 888}
		_, err := fixture.dispatcher.Handle(ctx, 1, source)
		mustGuest(t, err)
		var row d.Downstream
		mustGuest(t, fixture.db.Where("update_id = ?", source.ID).First(&row).Error)
		if row.UserID != 42+(thread-20) || row.ReplyMappingID != 0 {
			t.Fatal("unregistered reference crossed customer topic", row.UserID)
		}
	}
}

// TestStaffUnsupportedFeedbackIsDurableAndContentFree verifies same-topic feedback,
// stable queue deduplication and final route protection without delivering to customers.
func TestStaffUnsupportedFeedbackIsDurableAndContentFree(t *testing.T) {
	for _, protected := range []bool{false, true} {
		fixture, _, ctx := topicReplyFixture(t)
		source := topicStaffMessage(600, 20)
		source.HasProtectedContent = protected
		if !protected {
			source.MessageKind = "video_note"
			source.Text = ""
			source.Media = []r.Media{{Type: "video_note", FileID: "synthetic"}}
		}
		for attempt := range 2 {
			_, err := fixture.dispatcher.Handle(ctx, 1, source)
			if err != nil {
				t.Fatalf("protected=%v attempt=%d: %v", protected, attempt, err)
			}
		}
		var rows []rd.Delivery
		mustGuest(t, fixture.db.Where("purpose = ?", "support_reply_rejected").Find(&rows).Error)
		if len(rows) != 1 || rows[0].ChatID != -100 {
			t.Fatal("missing or duplicated staff feedback", len(rows))
		}
		envelope := gateEnvelope(t, fixture, rows[0])
		if envelope.ThreadID != 20 || envelope.Text == "" || envelope.Text == source.Text || envelope.Transfer != nil {
			t.Fatal("unsafe rejection feedback")
		}
		mustGuest(t, fixture.repo.AuthorizeTargetAt(ctx, 1, envelope, time.Now().UTC()))
		envelope.ThreadID = 21
		if err := fixture.repo.AuthorizeTargetAt(ctx, 1, envelope, time.Now().UTC()); err == nil {
			t.Fatal("feedback crossed topic")
		}
		var count int64
		mustGuest(t, fixture.db.Model(&rd.Delivery{}).Where("purpose = ?", "support_reply").Count(&count).Error)
		if count != 0 {
			t.Fatal("rejected content sent to customer")
		}
	}
}
