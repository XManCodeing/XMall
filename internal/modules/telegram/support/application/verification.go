package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"reflect"
	"strconv"
	"strings"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// arithmeticPNG renders persisted arithmetic directly to PNG with the existing font library.
// The deterministic image permits byte-for-byte authorization without storing plaintext media.
func arithmeticPNG(question string) ([]byte, error) {
	if question == "" || len(question) > 64 {
		return nil, c.ErrDenied
	}
	bounds, _ := font.BoundString(basicfont.Face7x13, question)
	ink := image.Rect(bounds.Min.X.Floor(), bounds.Min.Y.Floor(), bounds.Max.X.Ceil(), bounds.Max.Y.Ceil())
	if ink.Empty() {
		return nil, c.ErrDenied
	}
	const width, height, padding = 320, 100, 20
	scale := min(3, (width-2*padding)/ink.Dx(), (height-2*padding)/ink.Dy())
	if scale < 1 {
		return nil, c.ErrDenied
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{245, 247, 250, 255}), image.Point{}, draw.Src)
	glyphs := image.NewRGBA(image.Rect(0, 0, ink.Dx(), ink.Dy()))
	painter := font.Drawer{Dst: glyphs, Src: image.NewUniform(color.RGBA{30, 42, 55, 255}), Face: basicfont.Face7x13, Dot: fixed.P(-ink.Min.X, -ink.Min.Y)}
	painter.DrawString(question)
	// Center the measured ink, not a fixed glyph canvas. Keep all scaled digits
	// inside a symmetric safe area, including wider division and multiplication.
	left, top := (width-ink.Dx()*scale)/2, (height-ink.Dy()*scale)/2
	for vertical := 0; vertical < glyphs.Bounds().Dy(); vertical++ {
		for horizontal := 0; horizontal < glyphs.Bounds().Dx(); horizontal++ {
			pixel := glyphs.RGBAAt(horizontal, vertical)
			if pixel.A == 0 {
				continue
			}
			x, y := left+horizontal*scale, top+vertical*scale
			draw.Draw(canvas, image.Rect(x, y, x+scale, y+scale), image.NewUniform(pixel), image.Point{}, draw.Src)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

// noticeMessage derives every allowed envelope field from a durable, scoped notice.
// ctx/bot bind the lookup; malformed legacy challenges fail closed rather than single-click pass.
func (s *Service) noticeMessage(ctx context.Context, bot uint, notice c.Notice) (r.Message, error) {
	message := r.Message{TargetType: r.TargetSupportPrivate, BusinessRef: notice.Ref, ChatID: notice.ChatID, Operation: "send", TargetKind: "text", Purpose: "support_gate", Text: feedback(notice.Locale, notice.Reason)}
	if notice.Kind != "challenge" || notice.Reason != "challenge" {
		return message, nil
	}
	challenge, err := s.Store.Challenge(ctx, notice.Ref)
	if err != nil {
		return r.Message{}, err
	}
	labels := strings.Split(challenge.Choices, ",")
	if challenge.BotID != bot || challenge.UserID != notice.UserID || challenge.ChatID != notice.ChatID || challenge.State != "pending" || !s.Now().Before(challenge.ExpiresAt) || challenge.Question == "" || len(labels) != 5 || challenge.AnswerIndex < 0 || challenge.AnswerIndex > 4 {
		return r.Message{}, c.ErrDenied
	}
	photo, err := arithmeticPNG(challenge.Question)
	if err != nil {
		return r.Message{}, err
	}
	message.TargetKind, message.Photo = "photo", photo
	buttons := make([]r.Button, 5)
	for index, label := range labels {
		buttons[index] = r.Button{Text: label, Data: "support:" + c.VerificationAnswer(*challenge, index)}
	}
	message.Buttons = [][]r.Button{buttons[:3], buttons[3:]}
	return message, nil
}

// verificationCleanupMessage derives a content-free deletion from a persisted callback.
// update identifies the exact private chat/message; the return value is also the authorization template.
func verificationCleanupMessage(update r.Update) r.Message {
	return r.Message{TargetType: r.TargetSupportPrivate, BusinessRef: fmt.Sprintf("support:gate-cleanup:%d", update.ID), ChatID: update.ChatID, Purpose: "support_gate_cleanup", Operation: "delete", TargetMessageID: update.MessageID}
}

// enqueueVerificationCleanup queues one idempotent deletion for bot/update after verification.
// Errors propagate to the existing inbox retry, which reuses the persisted verification result.
func (s *Service) enqueueVerificationCleanup(ctx context.Context, bot uint, update r.Update) error {
	store, ok := s.Store.(c.VerificationCleanupStore)
	if !ok || s.Deliveries == nil {
		return c.ErrDenied
	}
	source, err := store.VerificationCleanup(ctx, bot, update.ID)
	if err != nil {
		return err
	}
	message := verificationCleanupMessage(source)
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	payload, err := s.Cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	key := fmt.Sprintf("support:gate-cleanup:%d:%d", source.ChatID, source.MessageID)
	err = s.Deliveries.EnqueueRequest(ctx, &r.DeliveryRequest{BotID: bot, Key: key, ChatID: source.ChatID, TelegramUserID: source.UserID, TargetType: string(r.TargetSupportPrivate), PayloadCipher: payload, Priority: r.PrioritySupport, Purpose: message.Purpose, Operation: message.Operation, Status: "pending", AvailableAt: s.Now().UTC()})
	if err != nil {
		slog.WarnContext(ctx, "support_verification_cleanup_enqueue_failed", "bot_id", bot, "update_id", update.ID)
		return err
	}
	slog.InfoContext(ctx, "support_verification_cleanup_enqueued", "bot_id", bot, "update_id", update.ID)
	return nil
}

// authorizeVerificationCleanup proves bot/message against the exact persisted successful callback.
// Extra fields, cross-customer targets and unverified callbacks return a denial.
func (s *Service) authorizeVerificationCleanup(ctx context.Context, bot uint, message r.Message) error {
	store, ok := s.Store.(c.VerificationCleanupStore)
	if !ok || !strings.HasPrefix(message.BusinessRef, "support:gate-cleanup:") {
		return c.ErrDenied
	}
	updateID, err := strconv.ParseInt(strings.TrimPrefix(message.BusinessRef, "support:gate-cleanup:"), 10, 64)
	if err != nil || updateID <= 0 {
		return c.ErrDenied
	}
	source, err := store.VerificationCleanup(ctx, bot, updateID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(verificationCleanupMessage(source), message) {
		return c.ErrDenied
	}
	return nil
}

// AuthorizeTarget compares the complete envelope, including PNG and buttons, to persisted intent.
// It denies unknown fields/capabilities as well as expired, blocked or altered notices.
func (s *Service) AuthorizeTarget(ctx context.Context, bot uint, message r.Message) error {
	if message.TargetType != r.TargetSupportPrivate || message.ChatID <= 0 {
		return c.ErrDenied
	}
	if message.Purpose == "support_gate_cleanup" {
		return s.authorizeVerificationCleanup(ctx, bot, message)
	}
	notice, err := s.Store.Notice(ctx, bot, message.BusinessRef)
	if err != nil {
		return err
	}
	if notice.ChatID != message.ChatID || notice.UserID != message.ChatID {
		return c.ErrDenied
	}
	if err = s.Store.CheckNotice(ctx, notice, s.Now().UTC()); err != nil {
		return err
	}
	expected, err := s.noticeMessage(ctx, bot, notice)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, message) {
		return c.ErrDenied
	}
	return nil
}
