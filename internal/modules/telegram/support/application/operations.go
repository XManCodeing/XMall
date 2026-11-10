package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// OperationService accepts only explicit staff actions and actor-bound callbacks.
// It has no admin authority and never emits a customer delivery for internal notes.
type OperationService struct {
	Store    c.Operations
	Members  c.SupportMembers
	Cipher   c.Cipher
	Now      func() time.Time
	Controls c.OperationControls
	Queue    ReadyQueue
	// BotUsername resolves the current bot username for explicitly addressed commands.
	BotUsername func(context.Context, uint) (string, error)
}

// Prepare returns callback data for an explicit UI action in a trusted topic.
// Parameters are the current bot and authenticated Telegram update; no IDs from
// message text are used. The result is opaque, short-lived and bound to this actor.
func (service *OperationService) Prepare(ctx context.Context, bot uint, intent c.OperationIntent) (string, error) {
	if service == nil || service.Store == nil || service.Members == nil || service.Now == nil || !c.ValidOperation(intent.Action) || !validStaffUpdate(bot, intent.Update) {
		return "", c.ErrDenied
	}
	note := strings.TrimSpace(intent.Note)
	if (intent.Action == c.OperationNote && (note == "" || len(note) > 4096)) || (intent.Action != c.OperationNote && note != "") {
		return "", c.ErrDenied
	}
	route, err := service.Members.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return "", err
	}
	if route.ChatID != intent.Update.ChatID {
		return "", c.ErrDenied
	}
	if err = service.authorize(ctx, bot, intent.Update, route.RouteVersion, route.Generation); err != nil {
		return "", err
	}
	encrypted := ""
	if note != "" {
		if service.Cipher == nil {
			return "", c.ErrDenied
		}
		encrypted, err = service.Cipher.Seal(note)
		if err != nil {
			return "", err
		}
	}
	token := make([]byte, 18)
	if _, err = rand.Read(token); err != nil {
		return "", err
	}
	ref := base64.RawURLEncoding.EncodeToString(token)
	row, err := service.Store.PrepareOperation(ctx, bot, intent.Update, route.RouteVersion, route.Generation, intent.Action, encrypted, ref, service.Now().UTC())
	if err != nil {
		return "", err
	}
	return c.OperationCallbackPrefix + row.Ref, nil
}

// Handle executes an opaque operation callback and returns only safe feedback.
// Membership is queried on every click, including duplicates; removed staff fail.
func (service *OperationService) Handle(ctx context.Context, bot uint, update r.Update) ([]r.Message, error) {
	if !strings.HasPrefix(update.CallbackData, c.OperationCallbackPrefix) {
		return nil, service.handleCommand(ctx, bot, update)
	}
	err := service.execute(ctx, bot, update)
	reason := "operation_done"
	if err != nil {
		reason = "operation_denied"
	}
	r.AnswerCallback(ctx, feedback(update.Locale, reason))
	if errors.Is(err, c.ErrDenied) || errors.Is(err, c.ErrConflict) {
		return nil, nil
	}
	return nil, err
}

// execute validates the trusted update, current live actor and runtime fence
// before delegating all database changes and their audit to the atomic store port.
func (service *OperationService) execute(ctx context.Context, bot uint, update r.Update) error {
	if service == nil || service.Store == nil || service.Members == nil || service.Now == nil || !validStaffUpdate(bot, update) || update.CallbackID == "" {
		return c.ErrDenied
	}
	ref := strings.TrimPrefix(update.CallbackData, c.OperationCallbackPrefix)
	if len(ref) != 24 {
		return c.ErrDenied
	}
	row, err := service.Store.Operation(ctx, bot, ref)
	if err != nil {
		return err
	}
	if row.ControlRef != "" {
		controls, ok := service.Store.(c.VerificationControls)
		if !ok || row.ActorID != 0 || row.Action != c.OperationBlock || row.ChatID != update.ChatID || row.ThreadID != update.ThreadID || !service.Now().Before(row.ExpiresAt) {
			return c.ErrDenied
		}
		route, routeErr := service.Members.ResolveSupportRoute(ctx, bot)
		if routeErr != nil {
			return routeErr
		}
		if err = service.authorize(ctx, bot, update, row.RouteVersion, route.Generation); err != nil {
			return err
		}
		err = controls.ApplyVerificationOperation(ctx, bot, update, ref, route.Generation, service.Now().UTC())
		slog.InfoContext(ctx, "support_verification_control_result", "bot_id", bot, "allowed", err == nil)
		return err
	}
	if row.ActorID != update.UserID || row.ChatID != update.ChatID || row.ThreadID != update.ThreadID || !service.Now().Before(row.ExpiresAt) {
		return c.ErrDenied
	}
	if err = service.authorize(ctx, bot, update, row.RouteVersion, row.Generation); err != nil {
		return err
	}
	return service.Store.ApplyOperation(ctx, bot, update, ref, service.Now().UTC())
}

// authorize performs external live membership checks outside database transactions.
// Both pre/post execution fences prevent stale runtime workers from mutating state.
func (service *OperationService) authorize(ctx context.Context, bot uint, update r.Update, version, generation uint64) error {
	route, err := service.Members.ResolveSupportRoute(ctx, bot)
	if err != nil {
		return err
	}
	if route.ChatID != update.ChatID || route.RouteVersion != version || route.Generation != generation {
		return c.ErrDenied
	}
	if err = r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return err
	}
	if err = service.Members.AuthorizeSupportMember(ctx, bot, update.ChatID, update.UserID, version); err != nil {
		return err
	}
	return r.CheckExecutionFence(ctx, bot, generation)
}

// IsOperationCommand reserves only the explicit /support command namespace.
// Every reserved command is consumed even if malformed, so a replied note cannot escape.
func IsOperationCommand(update r.Update) bool {
	fields := strings.Fields(update.Text)
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "/support" || strings.HasPrefix(fields[0], "/support@")
}

// handleCommand turns a real forum message into a persistent actor-bound menu.
// /support note <text> prepares an internal-note confirmation, never customer output.
func (service *OperationService) handleCommand(ctx context.Context, bot uint, update r.Update) error {
	if !IsOperationCommand(update) {
		return nil
	}
	if service.Controls == nil || service.Queue == nil || service.Cipher == nil || service.Now == nil || update.Type != "message" || update.ID <= 0 || update.MessageID <= 0 || update.CallbackID != "" || !validStaffUpdate(bot, update) {
		return c.ErrDenied
	}
	fields := strings.Fields(update.Text)
	if strings.Contains(fields[0], "@") {
		if service.BotUsername == nil {
			return c.ErrDenied
		}
		username, err := service.BotUsername(ctx, bot)
		if err != nil {
			return err
		}
		if !strings.EqualFold(fields[0], "/support@"+username) {
			return nil
		}
	}
	if previous, err := service.Controls.ControlForUpdate(ctx, bot, update.ID); err != nil {
		return err
	} else if previous != nil {
		if previous.ActorID != update.UserID || previous.ChatID != update.ChatID || previous.ThreadID != update.ThreadID {
			return c.ErrDenied
		}
		return service.enqueueControl(ctx, bot, *previous)
	}
	actions := []string{c.OperationUnblock, c.OperationClose, c.OperationBlock}
	note := ""
	if len(fields) > 1 {
		if fields[1] != "note" || len(fields) < 3 {
			return c.ErrDenied
		}
		note = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(update.Text, fields[0])), "note"))
		actions = []string{c.OperationNote}
	}
	callbacks := []string{}
	buttons := [][]r.Button{}
	for _, action := range actions {
		callback, err := service.Prepare(ctx, bot, c.OperationIntent{Update: update, Action: action, Note: note})
		// Closed sessions still allow unblock; no other failed preparation is hidden.
		if errors.Is(err, c.ErrDenied) && len(callbacks) > 0 && note == "" {
			continue
		}
		if err != nil {
			return err
		}
		callbacks = append(callbacks, callback)
		buttons = append(buttons, []r.Button{{Text: operationLabel(update.Locale, action), Data: callback}})
	}
	row, err := service.Store.Operation(ctx, bot, strings.TrimPrefix(callbacks[0], c.OperationCallbackPrefix))
	if err != nil {
		return err
	}
	refBytes := make([]byte, 18)
	if _, err = rand.Read(refBytes); err != nil {
		return err
	}
	ref := base64.RawURLEncoding.EncodeToString(refBytes)
	message := r.Message{TargetType: r.TargetSupportGroup, Purpose: c.OperationControlPurpose, Operation: "send", TargetKind: "text", ChatID: update.ChatID, ThreadID: update.ThreadID, RouteVersion: row.RouteVersion, BusinessRef: "support:control:" + ref, Text: operationLabel(update.Locale, "menu"), Buttons: buttons}
	control, err := service.Controls.SaveControl(ctx, bot, update, ref, callbacks, message, service.Now().UTC())
	if err != nil {
		return err
	}
	return service.enqueueControl(ctx, bot, control)
}

// enqueueControl restores the same immutable runtime key after inbox retries.
func (service *OperationService) enqueueControl(ctx context.Context, bot uint, control d.OperationControl) error {
	plain, err := service.Cipher.Open(control.PayloadCipher)
	if err != nil {
		return c.ErrDenied
	}
	var message r.Message
	if json.Unmarshal([]byte(plain), &message) != nil {
		return c.ErrDenied
	}
	if err = service.AuthorizeTarget(ctx, bot, message); err != nil {
		return err
	}
	return service.Queue.EnqueueDelivery(ctx, r.DeliveryRequest{BotID: bot, Key: message.BusinessRef, ChatID: control.ChatID, TelegramUserID: control.ActorID, PayloadCipher: control.PayloadCipher, TargetType: string(r.TargetSupportGroup), Purpose: c.OperationControlPurpose, Operation: "send", Status: "pending", Priority: r.PrioritySupport, AvailableAt: service.Now().UTC()})
}

// AuthorizeTarget proves the exact stored menu and rechecks live membership twice fenced.
func (service *OperationService) AuthorizeTarget(ctx context.Context, bot uint, message r.Message) error {
	if service.Controls == nil || service.Now == nil {
		return c.ErrDenied
	}
	control, err := service.Controls.CheckControl(ctx, bot, message, service.Now().UTC())
	if err != nil {
		return err
	}
	if control.DailyKey != nil {
		if service.Members == nil {
			return c.ErrDenied
		}
		route, routeErr := service.Members.ResolveSupportRoute(ctx, bot)
		if routeErr != nil {
			return routeErr
		}
		if route.ChatID != control.ChatID || route.RouteVersion != control.RouteVersion {
			return c.ErrDenied
		}
		if err = r.CheckExecutionFence(ctx, bot, route.Generation); err != nil {
			return err
		}
		_, err = service.Controls.CheckControl(ctx, bot, message, service.Now().UTC())
		return err
	}
	update := r.Update{ChatID: control.ChatID, ThreadID: control.ThreadID, UserID: control.ActorID}
	if err = service.authorize(ctx, bot, update, control.RouteVersion, control.Generation); err != nil {
		return err
	}
	_, err = service.Controls.CheckControl(ctx, bot, message, service.Now().UTC())
	if err != nil {
		return err
	}
	return r.CheckExecutionFence(ctx, bot, control.Generation)
}

// TickVerificationCards provisions topics and queues identity-only cards for today's interceptions.
// Cursor and limit bound work; no accepted customer content is released by this path.
func (service *OperationService) TickVerificationCards(ctx context.Context, bot uint, generation uint64, after uint, topics *TopicWorker) (uint, error) {
	controls, ok := service.Store.(c.VerificationControls)
	if !ok || topics == nil || service.Now == nil || service.Queue == nil {
		return after, c.ErrDenied
	}
	if err := r.CheckExecutionFence(ctx, bot, generation); err != nil {
		return after, err
	}
	rows, err := controls.VerificationSessions(ctx, bot, after, 50, service.Now().UTC())
	if err != nil {
		return after, err
	}
	for _, owner := range rows {
		control, controlErr := controls.EnsureVerificationControl(ctx, bot, owner.ID, generation, service.Now().UTC())
		if errors.Is(controlErr, c.ErrDenied) {
			// Existing cards avoid touching topic/session revisions on every scheduler tick.
			if err = topics.Ensure(ctx, bot, owner.ID); err != nil && !errors.Is(err, c.ErrDenied) {
				return after, err
			}
			if err == nil {
				control, controlErr = controls.EnsureVerificationControl(ctx, bot, owner.ID, generation, service.Now().UTC())
			}
		}
		if controlErr != nil && !errors.Is(controlErr, c.ErrDenied) {
			return after, controlErr
		}
		if controlErr == nil && control != nil && control.ConsumedAt == nil {
			if err = service.enqueueControl(ctx, bot, *control); err != nil && !errors.Is(err, c.ErrDenied) {
				return after, err
			}
		}
		after = owner.ID
	}
	if len(rows) < 50 {
		return 0, nil
	}
	return after, nil
}

// operationLabel reuses centralized translations and never includes consultation content.
func operationLabel(locale, action string) string {
	return feedback(locale, "operation_"+action)
}

// validStaffUpdate accepts attributable forum actors, never anonymous sender_chat.
func validStaffUpdate(bot uint, update r.Update) bool {
	return bot != 0 && !update.Private && update.ChatID < 0 && update.UserID > 0 && update.ThreadID > 0 && update.IsTopicMessage && update.SenderChat == nil && (update.ChatType == "supergroup" || update.ChatType == "")
}
