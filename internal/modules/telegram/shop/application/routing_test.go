package application

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type routingIdentity struct {
	*backend
	creates    int
	sent       int
	generation string
}

func (b *routingIdentity) Resolve(_ context.Context, _ c.Actor, create bool) (uint, error) {
	if create {
		b.creates++
	}
	return b.user, nil
}
func (b *routingIdentity) Generation(context.Context, c.Actor) (string, error) {
	return b.generation, nil
}
func (b *routingIdentity) SendBindCode(context.Context, c.Actor, string) error { b.sent++; return nil }

func putRoutingState(t *testing.T, s *Service, m *memoryStore, st state, expires time.Time) {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := crypto.Encrypt(s.key, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	m.sessions[sessionKey(1, 11, 11)] = rd.Session{BotID: 1, TelegramUserID: 11, ChatID: 11, State: st.Step, DataCipher: cipher, ExpiresAt: expires}
}

// TestSupportUnavailableSendsNotice preserves the shop panel and replays only the same update.
func TestSupportUnavailableSendsNotice(t *testing.T) {
	for _, user := range []uint{0, 7} {
		t.Run(fmt.Sprint(user), func(t *testing.T) {
			service, store, identity := setup()
			identity.user = user
			ctx := context.Background()
			run(t, service, update(1, "/start"))
			key := sessionKey(1, 11, 11)
			row := store.sessions[key]
			row.PanelMessageID = 130
			row.PanelMessageEpoch = row.PanelEpoch
			store.sessions[key] = row
			before := row.PanelRevision
			for _, id := range []int64{2, 3} {
				message := update(id, "synthetic consultation")
				out, err := service.HandleSupportUnavailable(ctx, 1, message)
				if err != nil || len(out) != 1 {
					t.Fatal(out, err)
				}
				if out[0].Purpose != "public_notice" || out[0].Operation != "send" || out[0].TargetMessageID != 0 || !strings.Contains(out[0].Text, "客服暂不可用") {
					t.Fatalf("consultation notice silently edited panel: %+v", out[0])
				}
				if store.sessions[key].PanelRevision != before {
					t.Fatal("notice replaced shop panel")
				}
				if user == 0 {
					if err := service.AuthorizePublicReply(ctx, 1, out[0]); err != nil {
						t.Fatal(err)
					}
				}
				replay, err := service.HandleSupportUnavailable(ctx, 1, message)
				originalJSON, originalErr := json.Marshal(out)
				replayJSON, replayErr := json.Marshal(replay)
				if err != nil || originalErr != nil || replayErr != nil || string(originalJSON) != string(replayJSON) {
					t.Fatal("retry changed notice", err)
				}
			}
		})
	}
}

func TestLegacyRoutingQuarantineSurvivesRetryExpiryAndIdentityChange(t *testing.T) {
	for _, kind := range []string{"email", "code", "quantity", "identity", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			s, m, b := setup()
			identity := &routingIdentity{backend: b, generation: "new"}
			s.d.Identity = identity
			s.d.Content = bindingContent{url: "https://shop.example/me/security"}
			now := time.Now()
			s.now = func() time.Time { return now }
			st := state{Step: kind, UserID: 7, Generation: "new", Email: "old@example.test"}
			expires := now.Add(-time.Minute)
			if kind == "identity" {
				st.Step = "code"
				st.Generation = "old"
				expires = now.Add(time.Hour)
			}
			putRoutingState(t, s, m, st, expires)
			if kind == "corrupt" {
				row := m.sessions[sessionKey(1, 11, 11)]
				row.DataCipher = "broken"
				m.sessions[sessionKey(1, 11, 11)] = row
			}
			u := update(10, "secret-input")
			m.fail = true
			if _, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u); err == nil || !handled {
				t.Fatal("CAS failure fell through")
			}
			got, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u)
			if err != nil || !handled || len(got) != 1 || len(got[0].Buttons) != 1 {
				t.Fatal(got, handled, err)
			}
			if strings.Contains(got[0].Text, "secret-input") || got[0].Buttons[0][0].URL != "https://shop.example/me/security" {
				t.Fatal("input leaked or website missing")
			}
			now = now.Add(time.Hour)
			replay, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u)
			if err != nil || !handled || !reflect.DeepEqual(got, replay) {
				t.Fatal("expired retry was reclassified", handled, err)
			}
			if identity.creates != 0 || identity.sent != 0 || len(m.contacts) != 0 {
				t.Fatal("quarantine created identity/contact or sent code")
			}
			if _, handled, err := s.ConsumeLegacyInput(context.Background(), 1, update(11, "new consultation")); err != nil || handled {
				t.Fatal("next consultation blocked", err)
			}
			// A subsequent shopping command must preserve the high-water fence.
			run(t, s, update(12, "/start"))
			if _, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u); err != nil || !handled {
				t.Fatal("shopping command erased fence", err)
			}
		})
	}
}

func TestLegacyRoutingOrdinaryConsultationDoesNotResolveOrCreate(t *testing.T) {
	s, m, _ := setup()
	s.d.Identity = nil // Any identity access is a test failure.
	if _, handled, err := s.ConsumeLegacyInput(context.Background(), 1, update(1, "question")); err != nil || handled {
		t.Fatal(handled, err)
	}
	putRoutingState(t, s, m, state{UserID: 7}, time.Now().Add(time.Hour))
	if _, handled, err := s.ConsumeLegacyInput(context.Background(), 1, update(2, "question")); err != nil || handled {
		t.Fatal(handled, err)
	}
	if len(m.contacts) != 0 {
		t.Fatal("consultation created contact")
	}
}

func TestLegacyRoutingActiveEmailAndQuantityRemainCompatible(t *testing.T) {
	s, m, b := setup()
	identity := &routingIdentity{backend: b, generation: "same"}
	s.d.Identity = identity
	putRoutingState(t, s, m, state{Step: "email", UserID: 7, Generation: "same"}, time.Now().Add(time.Hour))
	u := update(10, "user@example.test")
	first, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u)
	if err != nil || !handled || identity.sent != 1 {
		t.Fatal(handled, err, identity.sent)
	}
	replay, handled, err := s.ConsumeLegacyInput(context.Background(), 1, u)
	if err != nil || !handled || identity.sent != 1 || !reflect.DeepEqual(first, replay) {
		t.Fatal("active email replay changed", err)
	}
	// Valid quantity sessions restore buttons, never consume the number as support.
	putRoutingState(t, s, m, state{Step: "quantity", UserID: 7, Generation: "same", Selection: c.Selection{ProductID: 2, Quantity: 1}}, time.Now().Add(time.Hour))
	out, handled, err := s.ConsumeLegacyInput(context.Background(), 1, update(11, "17"))
	if err != nil || !handled || len(out) == 0 {
		t.Fatal(out, handled, err)
	}
	row, _ := m.GetSession(context.Background(), 1, 11, 11)
	plain, _ := crypto.Decrypt(s.key, row.DataCipher)
	var st state
	if err := json.Unmarshal([]byte(plain), &st); err != nil {
		t.Fatal(err)
	}
	if st.Selection.Quantity != 1 || st.Step != "" || st.LegacyInputThrough != 11 {
		t.Fatal("quantity transition corrupted selection", st.Selection)
	}
}
