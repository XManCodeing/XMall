package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/XManCodeing/XMall/internal/crypto"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type supportUnavailableKey struct{}

// HandleSupportUnavailable returns a persisted, authorized notice for an ordinary
// private message after the dispatcher has excluded commands and legacy input.
// It never forwards the user's text or claims that a consultation was accepted.
func (s *Service) HandleSupportUnavailable(ctx context.Context, bot uint, update rt.Update) ([]rt.Message, error) {
	return s.handle(context.WithValue(ctx, supportUnavailableKey{}, true), bot, update, false)
}

func legacyStep(step string) bool { return step == "email" || step == "code" || step == "quantity" }

// ConsumeLegacyInput inspects persisted sessions without creating an identity or
// contact. Even expired sessions must be inspected before releasing text to support.
// Routing fences live in the encrypted session, not an in-memory retry cache.
func (s *Service) ConsumeLegacyInput(ctx context.Context, bot uint, u rt.Update) ([]rt.Message, bool, error) {
	if !u.Private || u.ChatID <= 0 || u.ChatID != u.UserID || bot == 0 {
		return nil, true, c.ErrDenied
	}
	if u.CallbackData != "" || strings.HasPrefix(strings.TrimSpace(u.Text), "/") {
		return nil, false, nil
	}
	row, err := s.d.Store.GetSession(ctx, bot, u.UserID, u.ChatID)
	if errors.Is(err, rt.ErrNotFound) || (err == nil && row == nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	st := &state{}
	corrupt := false
	if row.DataCipher != "" {
		plain, e := crypto.Decrypt(s.key, row.DataCipher)
		corrupt = e != nil
		if e == nil {
			corrupt = json.Unmarshal([]byte(plain), st) != nil
		}
	} else {
		corrupt = true
	}
	// A damaged payload cannot prove this was ordinary input. Quarantine it too.
	sensitive := corrupt || legacyStep(row.State) || legacyStep(st.Step)
	if !corrupt && u.ID > 0 && st.LegacyInputThrough >= u.ID {
		if st.LastUpdate == u.ID {
			return st.Reply, true, nil
		}
		return nil, true, nil
	}
	if !sensitive {
		return nil, false, nil
	}
	if u.ID <= 0 {
		// Without a durable inbox identity there is no safe replay fence.
		return nil, true, c.ErrDenied
	}
	actor := c.Actor{BotID: bot, TelegramID: u.UserID, Username: u.Username, UsernamePresent: u.Actor != nil && !u.Actor.IsBot, Locale: locale(u.Locale)}
	uid, err := s.d.Identity.Resolve(ctx, actor, false)
	failureKey := identityFailureKey(err)
	if err != nil && failureKey == "" {
		return nil, true, err
	}
	actor.UserID = uid
	if identity, ok := s.d.Identity.(c.IdentityGeneration); ok && uid != 0 {
		actor.Generation, err = identity.Generation(ctx, actor)
		if err != nil {
			return nil, true, err
		}
	}
	if !corrupt && failureKey == "" && uid != 0 && st.UserID == uid && st.Generation == actor.Generation && row.ExpiresAt.After(s.now()) {
		messages, err := s.handle(ctx, bot, u, true)
		return messages, true, err
	}
	// Never interpret this input after expiry, identity replacement or corruption.
	// A committed fence survives retry and contains no copy of the submitted secret.
	next := &state{UserID: uid, Generation: actor.Generation, IdentityFailure: failureKey, Locale: actor.Locale, LastUpdate: u.ID, LegacyInputThrough: u.ID}
	content := c.Content{}
	if s.d.Content != nil {
		content, err = s.d.Content.Content(ctx, actor)
		if err != nil {
			return nil, true, err
		}
	}
	message := rt.Message{ChatID: u.ChatID, OwnerUserID: uid, IdentityStamp: actor.Generation, Text: text(actor.Locale, "binding_unavailable")}
	if content.BindingURL != "" {
		message.Text = text(actor.Locale, "binding_website")
		message.Buttons = [][]rt.Button{{{Text: text(actor.Locale, "open_binding"), URL: content.BindingURL}}}
	}
	if failureKey != "" {
		message.Text, message.Buttons = text(actor.Locale, failureKey), nil
	}
	if uid == 0 {
		next.PublicGeneration, err = nonce()
		if err != nil {
			return nil, true, err
		}
		message.Purpose = "public_notice"
		message.TargetType = rt.TargetShopPrivate
		message.PublicReply = &rt.PublicReplyRef{BotID: bot, TelegramID: u.UserID, Generation: next.PublicGeneration, ExpiresAt: s.now().Add(30 * time.Minute)}
	}
	next.Reply = []rt.Message{message}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, true, err
	}
	cipher, err := crypto.Encrypt(s.key, string(raw))
	if err != nil {
		return nil, true, err
	}
	row.DataCipher, row.State = cipher, ""
	row.ExpiresAt = s.now().Add(30 * time.Minute)
	if err := s.d.Store.SaveSession(ctx, row, row.Version); err != nil {
		return nil, true, err
	}
	return next.Reply, true, nil
}
