package telegrambroadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	broadcastapp "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	runtimecontract "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	runtimedomain "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type Cipher interface {
	Seal(string) (string, error)
	Open(string) (string, error)
}

// IdentityResolver must return the current external identity's ID:userID stamp.
type IdentityResolver interface {
	IdentityStamp(context.Context, uint, int64, uint) (string, error)
}

// ShopIdentity adapts the shop's current binding checks without importing its application.
type ShopIdentity struct {
	Shop interface {
		AuthorizeDelivery(context.Context, uint, int64, uint) error
		DeliveryStamp(context.Context, uint, int64) (string, error)
	}
}

func (s ShopIdentity) IdentityStamp(ctx context.Context, bot uint, tg int64, user uint) (string, error) {
	if s.Shop == nil {
		return "", broadcastapp.ErrInvalid
	}
	if err := s.Shop.AuthorizeDelivery(ctx, bot, tg, user); err != nil {
		return "", err
	}
	return s.Shop.DeliveryStamp(ctx, bot, tg)
}

type RuntimeBridge struct {
	Store    runtimecontract.Store
	Cipher   Cipher
	Users    broadcastapp.UserDirectory
	Identity IdentityResolver
}
type recipient struct {
	UserID             uint
	TelegramID, ChatID int64
	Stamp              string
}
type snapshot struct {
	Recipients []recipient
	Message    runtimecontract.Message
}

func (b *RuntimeBridge) ListTelegramUsers(q broadcastapp.UserQuery) ([]broadcastapp.UserItem, int64, error) {
	if q.BotID == 0 || b.Store == nil || b.Users == nil {
		return nil, 0, broadcastapp.ErrInvalid
	}
	ctx := context.Background()
	bot, err := b.Store.Get(ctx, q.BotID)
	if err != nil {
		return nil, 0, err
	}
	if bot == nil || bot.Mode != "internal" {
		return nil, 0, broadcastapp.ErrInvalid
	}
	page, size := q.Page, q.PageSize
	q.Page = 1
	q.PageSize = 0
	items, _, err := b.Users.ListTelegramUsers(q)
	if err != nil {
		return nil, 0, err
	}
	result := make([]broadcastapp.UserItem, 0, len(items))
	for _, item := range items {
		id, e := strconv.ParseInt(item.TelegramUserID, 10, 64)
		if e != nil {
			continue
		}
		contact, e := b.Store.Contact(ctx, q.BotID, id)
		if errors.Is(e, runtimecontract.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		if contact != nil && contact.Reachable && contact.ChatID == id && id > 0 && contact.UserID == item.UserID && item.UserID != 0 {
			result = append(result, item)
		}
	}
	total := int64(len(result))
	if size > 0 {
		if page < 1 {
			page = 1
		}
		start := (page - 1) * size
		if start >= len(result) {
			return []broadcastapp.UserItem{}, total, nil
		}
		end := start + size
		if end > len(result) {
			end = len(result)
		}
		result = result[start:end]
	}
	return result, total, nil
}
func (b *RuntimeBridge) Prepare(ctx context.Context, input broadcastapp.CreateInput) (string, int, error) {
	// Unsupported filters must never silently broaden an audience to all users.
	if len(input.Filters) != 0 {
		return "", 0, broadcastapp.ErrInvalid
	}
	if b.Cipher == nil || b.Identity == nil {
		return "", 0, broadcastapp.ErrInvalid
	}
	// Runtime currently supports text/photos, not arbitrary file/HTML payloads.
	if input.AttachmentURL != "" || input.AttachmentName != "" {
		return "", 0, broadcastapp.ErrInvalid
	}
	q := broadcastapp.UserQuery{BotID: input.BotID}
	if input.RecipientType == "specific" {
		q.UserIDs = input.UserIDs
	}
	items, _, err := b.ListTelegramUsers(q)
	if err != nil {
		return "", 0, err
	}
	snap := snapshot{Message: runtimecontract.Message{Text: input.MessageHTML}}
	seen := map[int64]bool{}
	for _, item := range items {
		id, err := strconv.ParseInt(item.TelegramUserID, 10, 64)
		if err != nil {
			return "", 0, err
		}
		contact, err := b.Store.Contact(ctx, input.BotID, id)
		if err != nil {
			return "", 0, err
		}
		if contact == nil || !contact.Reachable || contact.UserID != item.UserID || contact.ChatID != id || id <= 0 || seen[contact.ChatID] {
			continue
		}
		stamp, err := b.Identity.IdentityStamp(ctx, input.BotID, id, item.UserID)
		if err != nil {
			return "", 0, err
		}
		if stamp == "" {
			return "", 0, broadcastapp.ErrInvalid
		}
		seen[contact.ChatID] = true
		snap.Recipients = append(snap.Recipients, recipient{item.UserID, id, contact.ChatID, stamp})
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", 0, err
	}
	cipher, err := b.Cipher.Seal(string(raw))
	return cipher, len(snap.Recipients), err
}
func (b *RuntimeBridge) Enqueue(ctx context.Context, job *broadcastdomain.Broadcast) error {
	bot, err := b.Store.Get(ctx, job.BotID)
	if err != nil {
		return err
	}
	if bot == nil || bot.Mode != "internal" {
		return broadcastapp.ErrInvalid
	}
	raw, err := b.Cipher.Open(job.PayloadCipher)
	if err != nil {
		return errors.New("broadcast snapshot unavailable")
	}
	var snap snapshot
	if err = json.Unmarshal([]byte(raw), &snap); err != nil {
		return errors.New("invalid broadcast snapshot")
	}
	if len(snap.Recipients) != job.RecipientCount {
		return errors.New("broadcast recipient count mismatch")
	}
	// Validate the entire snapshot before inserting any rows. A corrupt snapshot
	// must not leave a partially-sendable audience behind.
	seen := make(map[int64]bool, len(snap.Recipients))
	for _, r := range snap.Recipients {
		if r.Stamp == "" || r.UserID == 0 || r.TelegramID <= 0 || r.ChatID != r.TelegramID || seen[r.ChatID] {
			return broadcastapp.ErrInvalid
		}
		seen[r.ChatID] = true
	}
	for _, r := range snap.Recipients {
		message := snap.Message
		message.ChatID = r.ChatID
		message.OwnerUserID = r.UserID
		message.IdentityStamp = r.Stamp
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		cipher, err := b.Cipher.Seal(string(payload))
		if err != nil {
			return err
		}
		err = b.Store.Enqueue(ctx, &runtimedomain.Delivery{BotID: job.BotID, Key: fmt.Sprintf("broadcast:%d:chat:%d", job.ID, r.ChatID), ChatID: r.ChatID, TelegramUserID: r.TelegramID, UserID: r.UserID, BroadcastID: job.ID, PayloadCipher: cipher, Priority: 1, Status: "pending", AvailableAt: time.Now()})
		if err != nil {
			return err
		}
	}
	return nil
}
func (b *RuntimeBridge) Progress(ctx context.Context, botID, broadcastID uint) (broadcastapp.Progress, error) {
	var p broadcastapp.Progress
	for page := 1; ; page++ {
		items, total, err := b.Store.ListDeliveries(ctx, botID, broadcastID, page, 200)
		if err != nil {
			return p, err
		}
		for _, d := range items {
			p.Total++
			switch d.Status {
			case "sent":
				p.Sent++
			case "failed", "cancelled":
				p.Failed++
			case "unknown":
				p.Unknown++
			default:
				p.Pending++
			}
		}
		if int64(p.Total) >= total || len(items) == 0 {
			break
		}
	}
	return p, nil
}

func (b *RuntimeBridge) ListBroadcastDeliveries(ctx context.Context, botID, broadcastID uint, page, size int) ([]broadcastapp.DeliveryItem, int64, error) {
	if b.Store == nil || botID == 0 || broadcastID == 0 {
		return nil, 0, broadcastapp.ErrInvalid
	}
	rows, total, err := b.Store.ListDeliveries(ctx, botID, broadcastID, page, size)
	if err != nil {
		return nil, 0, err
	}
	items := make([]broadcastapp.DeliveryItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, broadcastapp.DeliveryItem{
			ID: row.ID, BotID: row.BotID, BroadcastID: row.BroadcastID,
			UserID: row.UserID, TelegramUserID: row.TelegramUserID, ChatID: row.ChatID,
			Status: row.Status, Attempts: row.Attempts, MessageID: row.MessageID,
			LastError: row.LastError, Retryable: runtimedomain.CanRetryBroadcastDelivery(row),
			AvailableAt: row.AvailableAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return items, total, nil
}
