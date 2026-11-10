package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/google/uuid"
)

const failureNoticePurpose = "runtime_failure"

var errProcessingPanic = errors.New("update processing panic")
var errProcessingUnknown = errors.New("update processing outcome unknown")

// recoverWorker contains an unexpected worker exit and cancels its generation.
// The parent supervisor joins every sibling before acquiring a new generation.
// Panic values are omitted because they can contain credentials.
func recoverWorker(ctx context.Context, worker string, cancel context.CancelFunc) {
	if recover() != nil {
		slog.ErrorContext(ctx, "telegram worker panic", "worker", worker)
		cancel()
	}
}

// startWorker registers a generation-owned goroutine; unexpected panic cancels
// siblings so the outer supervisor can restart after all children have joined.
func startWorker(ctx context.Context, workers *sync.WaitGroup, name string, cancel context.CancelFunc, action func()) {
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer recoverWorker(ctx, name, cancel)
		action()
	}()
}

// recoverCall converts an action panic to a safe error, never exposing its value.
// Callers decide whether an external action may have executed before the panic.
func recoverCall(action func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errProcessingPanic
		}
	}()
	return action()
}

// listInstances keeps the supervisor alive after a store panic. A failed lookup
// preserves current children and returns an error for the next reconciliation.
func (r *Runtime) listInstances(ctx context.Context) (rows []d.Instance, err error) {
	err = recoverCall(func() error {
		var lookupErr error
		rows, lookupErr = r.manager.Store.List(ctx)
		return lookupErr
	})
	if err != nil {
		slog.WarnContext(ctx, "telegram supervisor listing unavailable", "error_class", failureErrorClass(err))
	}
	return rows, err
}

// recoverDelivery isolates one item. started distinguishes unsent work from an
// external attempt whose result cannot safely be inferred after panic.
func (r *Runtime) recoverDelivery(ctx context.Context, item *d.Delivery, claim string, started *bool) {
	if recover() == nil {
		return
	}
	status, detail := "retry", "update processing failed"
	if item.Attempts >= 8 {
		status = "failed"
	}
	if *started {
		status, detail = "unknown", "delivery outcome unknown"
	}
	slog.ErrorContext(ctx, "telegram delivery panic isolated", "bot_id", item.BotID, "delivery_id", item.ID, "state", status)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for {
		err := recoverCall(func() error {
			if panel, ok := r.manager.Store.(c.PanelStore); ok && item.Purpose == "panel" {
				return panel.FinishPanel(finishCtx, item, status, c.DeliveryResult{}, detail, time.Now().Add(5*time.Second))
			}
			return r.manager.Store.FinishDelivery(finishCtx, item.ID, claim, status, 0, detail, time.Now().Add(5*time.Second))
		})
		if err == nil || errors.Is(err, c.ErrStale) {
			return
		}
		if !wait(finishCtx, 100*time.Millisecond) {
			slog.ErrorContext(ctx, "telegram panic outcome persistence unavailable", "delivery_id", item.ID)
			return
		}
	}
}

// encodeUpdates builds an all-or-nothing encrypted batch for bot and offset.
// Failure returns the old offset, allowing retry without acknowledging data loss.
func (r *Runtime) encodeUpdates(bot uint, offset int64, updates []c.Update) (batch []d.Inbox, next int64, err error) {
	next = offset
	err = recoverCall(func() error {
		batch = make([]d.Inbox, 0, len(updates))
		for _, update := range updates {
			raw, encodeErr := json.Marshal(update)
			if encodeErr != nil {
				return encodeErr
			}
			cipher, sealErr := r.manager.Seal(string(raw))
			if sealErr != nil {
				return sealErr
			}
			batch = append(batch, d.Inbox{BotID: bot, UpdateID: update.ID, PayloadCipher: cipher, Status: "pending", AvailableAt: time.Now()})
			if update.ID >= next {
				next = update.ID + 1
			}
		}
		return nil
	})
	if err != nil {
		return nil, offset, err
	}
	return batch, next, nil
}

const failureNoticeLifetime = 15 * time.Minute

// processInbox consumes claimed updates for bot i using token. Business failures
// remain retryable for eight attempts; the independent notice never marks them done.
func (r *Runtime) processInbox(ctx context.Context, i *d.Instance, token string) {
	for ctx.Err() == nil {
		claim := uuid.NewString()
		entry, err := r.manager.Store.ClaimInbox(ctx, i.ID, claim, time.Now().Add(2*time.Minute))
		if err != nil || entry == nil {
			if !wait(ctx, 300*time.Millisecond) {
				return
			}
			continue
		}
		if r.checkRecovery(ctx, i) != nil {
			return
		}
		processCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = r.handleEntry(processCtx, i, token, entry)
		cancel()
		state, summary := "done", ""
		if err != nil {
			state = "retry"
			summary = "update processing failed"
			if entry.Attempts >= 8 {
				state = "failed"
			}
			if errors.Is(err, errProcessingUnknown) {
				state, summary = "unknown", "update processing outcome unknown"
			}
			slog.Warn("telegram update temporarily unavailable", "bot_id", i.ID, "update_id", entry.UpdateID, "error_class", failureErrorClass(err))
			// Use the worker context, not the already-cancelled processing timeout.
			// No failed-history scan or session write participates in this path.
			noticeCtx, noticeCancel := context.WithTimeout(ctx, 3*time.Second)
			noticeErr := recoverCall(func() error { return r.enqueueFailureNotice(noticeCtx, i, entry) })
			noticeCancel()
			if noticeErr != nil {
				slog.Warn("telegram failure notice unavailable", "bot_id", i.ID, "update_id", entry.UpdateID, "error_class", failureErrorClass(noticeErr))
			}
		}
		if finishErr := r.manager.Store.FinishInbox(ctx, entry.ID, claim, state, summary, time.Now().Add(5*time.Second)); finishErr != nil {
			slog.Warn("telegram inbox result unavailable", "bot_id", i.ID, "update_id", entry.UpdateID, "error_class", failureErrorClass(finishErr))
		}
	}
}

// failureErrorClass classifies err without serializing implementation errors,
// message bodies, credentials or account data. It returns a fixed safe label.
func failureErrorClass(err error) string {
	switch {
	case errors.Is(err, errProcessingPanic):
		return "processing_panic"
	case errors.Is(err, errProcessingUnknown):
		return "processing_outcome_unknown"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, c.ErrStale), errors.Is(err, c.ErrRecoveryBlocked):
		return "execution_fenced"
	case errors.Is(err, errTarget):
		return "authorization"
	default:
		return "dependency_or_processing"
	}
}

// failureNoticeText returns the complete static notice for locale; unknown
// locales use English. No user input or business content enters the message.
func failureNoticeText(locale string) string {
	switch locale {
	case "zh-CN", "zh", "zh-Hans":
		return "服务暂不可用，请稍后重试。"
	case "zh-TW", "zh-HK", "zh-Hant":
		return "服務暫不可用，請稍後重試。"
	default:
		return "Service temporarily unavailable. Please try again later."
	}
}

// failureNoticeMessage builds the only permitted unsigned envelope for recipient
// tg, reference ref and static text. All sensitive/interactive fields stay zero.
func failureNoticeMessage(tg int64, ref, text string) c.Message {
	return c.Message{TargetType: c.TargetShopPrivate, BusinessRef: ref, Purpose: failureNoticePurpose, Operation: "send", ChatID: tg, Text: text}
}

// failureNoticeSignature authenticates bot, recipient and the entire message
// (including update reference and expiry), excluding only the signature itself.
// It returns a domain-separated HMAC; no identity or session lookup is involved.
func (r *Runtime) failureNoticeSignature(bot uint, tg int64, message c.Message) (string, error) {
	message.IdentityStamp = ""
	payload, err := json.Marshal(struct {
		Domain  string
		BotID   uint
		UserID  int64
		Message c.Message
	}{"telegram-runtime-failure-v1", bot, tg, message})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, r.manager.key)
	_, _ = mac.Write(payload) // hash.Hash.Write cannot fail.
	return "failure-v1:" + hex.EncodeToString(mac.Sum(nil)), nil
}

// enqueueFailureNotice independently decrypts entry for bot i, verifies a real
// private update, and persists one notice per update. It returns infrastructure
// failures without changing the original inbox result or overwriting a session.
func (r *Runtime) enqueueFailureNotice(ctx context.Context, i *d.Instance, entry *d.Inbox) error {
	if entry.BotID != i.ID || entry.Status == "failed" || entry.Attempts > 8 {
		return errTarget
	}
	if err := r.checkRecovery(ctx, i); err != nil {
		return err
	}
	raw, err := r.manager.Open(entry.PayloadCipher)
	if err != nil {
		return err
	}
	var update c.Update
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		return err
	}
	if update.ID != entry.UpdateID || update.ID < 0 || !validUpdate(update) || !shopContactUpdate(update) {
		return errTarget
	}
	key := fmt.Sprintf("failure:%d", update.ID)
	ref := fmt.Sprintf("%s:%d", key, time.Now().Add(failureNoticeLifetime).Unix())
	message := failureNoticeMessage(update.UserID, ref, failureNoticeText(update.Locale))
	message.IdentityStamp, err = r.failureNoticeSignature(i.ID, update.UserID, message)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	cipher, err := r.manager.Seal(string(payload))
	if err != nil {
		return err
	}
	item := &d.Delivery{BotID: i.ID, Key: key, TargetType: string(c.TargetShopPrivate), ChatID: update.UserID, TelegramUserID: update.UserID, PayloadCipher: cipher, Purpose: failureNoticePurpose, Operation: "send", Priority: 100, Status: "pending", AvailableAt: time.Now()}
	if _, err := r.authorizeFailureNotice(ctx, i.ID, item, message); err != nil {
		return err
	}
	return r.manager.Store.Enqueue(ctx, item)
}

// authorizeFailureNotice handles only the runtime-reserved envelope. For ctx,
// bot, persisted item and decrypted message it returns (false,nil) for ordinary
// messages, or (true,error) for a validated/rejected notice. Owner zero alone
// never grants authorization. Call before all handler-dependent authorization.
func (r *Runtime) authorizeFailureNotice(ctx context.Context, bot uint, item *d.Delivery, message c.Message) (bool, error) {
	if message.Purpose != failureNoticePurpose && item.Purpose != failureNoticePurpose && !strings.HasPrefix(item.Key, "failure:") && !strings.HasPrefix(message.IdentityStamp, "failure-v1:") {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if bot == 0 || item.BotID != bot || item.UserID != 0 || item.ChatID <= 0 || item.ChatID != item.TelegramUserID ||
		item.TargetType != string(c.TargetShopPrivate) || item.Purpose != failureNoticePurpose || item.Operation != "send" ||
		item.BroadcastID != 0 || item.PanelEpoch != "" || item.PanelRevision != 0 || item.TargetMessageID != 0 || item.PreviousMessageID != 0 {
		return true, errTarget
	}
	parts := strings.Split(message.BusinessRef, ":")
	if len(parts) != 3 || parts[0] != "failure" || item.Key != strings.Join(parts[:2], ":") {
		return true, errTarget
	}
	updateID, updateErr := strconv.ParseInt(parts[1], 10, 64)
	expires, expiryErr := strconv.ParseInt(parts[2], 10, 64)
	now := time.Now()
	if updateErr != nil || updateID < 0 || parts[1] != strconv.FormatInt(updateID, 10) || expiryErr != nil ||
		expires <= now.Unix() || expires > now.Add(failureNoticeLifetime).Unix() {
		return true, errTarget
	}
	validText := false
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		validText = validText || message.Text == failureNoticeText(locale)
	}
	unsigned := message
	unsigned.IdentityStamp = ""
	if !validText || !reflect.DeepEqual(unsigned, failureNoticeMessage(item.TelegramUserID, message.BusinessRef, message.Text)) {
		return true, errTarget
	}
	expected, err := r.failureNoticeSignature(bot, item.TelegramUserID, message)
	if err != nil {
		return true, err
	}
	if !hmac.Equal([]byte(expected), []byte(message.IdentityStamp)) {
		return true, errTarget
	}
	return true, nil
}
