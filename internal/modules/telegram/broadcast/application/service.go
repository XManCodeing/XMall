package broadcastapp

import (
	"context"
	"errors"
	"strings"
	"time"

	broadcastcontract "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/contract"
	broadcastdomain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	notifycontract "github.com/XManCodeing/XMall/internal/modules/telegram/notify/contract"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
)

type ListInput struct{ Page, PageSize int }
type UserQuery struct {
	BotID                                                  uint
	Page, PageSize                                         int
	UserIDs                                                []uint
	Keyword, DisplayName, TelegramUsername, TelegramUserID string
	CreatedFrom, CreatedTo                                 *time.Time
}
type UserItem struct {
	UserID           uint      `json:"user_id"`
	DisplayName      string    `json:"display_name"`
	UserEmail        string    `json:"user_email"`
	TelegramUsername string    `json:"telegram_username"`
	TelegramUserID   string    `json:"telegram_user_id"`
	BoundAt          time.Time `json:"bound_at"`
	UserCreatedAt    time.Time `json:"user_created_at"`
}

// Button is a URL-only inline button for broadcast messages.
type Button struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type CreateInput struct {
	BotID                                      uint
	Title, RecipientType                       string
	UserIDs                                    []uint
	Filters                                    jsonmap.JSON
	AttachmentURL, AttachmentName, MessageHTML string
	Buttons                                    [][]Button
}
type UserDirectory interface {
	ListTelegramUsers(UserQuery) ([]UserItem, int64, error)
}

// Deprecated compatibility ports: credentials and direct sending are never used.
type BotTokenResolver interface{ ResolveActiveBotToken() (string, error) }
type Sender interface {
	SendWithBotToken(context.Context, string, notifycontract.SendOptions) error
}
type Dispatcher interface {
	DispatchBroadcast(context.Context, uint) (bool, error)
}

// DeliveryBridge snapshots only known bot contacts, seals identity stamps and
// enqueues idempotently into the runtime's single per-recipient delivery ledger.
type DeliveryBridge interface {
	Prepare(context.Context, CreateInput) (cipher string, recipients int, err error)
	Enqueue(context.Context, *broadcastdomain.Broadcast) error
	Progress(context.Context, uint, uint) (Progress, error)
}
type Progress struct{ Total, Sent, Failed, Unknown, Pending int }
type Service struct {
	store      broadcastcontract.Store
	users      UserDirectory
	deliveries DeliveryBridge
	dispatcher Dispatcher
}

func NewService(store broadcastcontract.Store, users UserDirectory, _ BotTokenResolver, dispatcher Dispatcher, _ Sender) *Service {
	return &Service{store: store, users: users, dispatcher: dispatcher}
}
func NewRuntimeService(store broadcastcontract.Store, users UserDirectory, deliveries DeliveryBridge, dispatcher Dispatcher) *Service {
	return &Service{store: store, users: users, deliveries: deliveries, dispatcher: dispatcher}
}
func (s *Service) GetBroadcast(ctx context.Context, id uint) (*broadcastdomain.Broadcast, error) {
	return s.store.GetByID(ctx, id)
}
func (s *Service) ListBroadcasts(ctx context.Context, i ListInput) ([]broadcastdomain.Broadcast, int64, error) {
	return s.store.List(ctx, broadcastcontract.ListFilter{Page: i.Page, PageSize: i.PageSize})
}
func (s *Service) ListTelegramUsers(q UserQuery) ([]UserItem, int64, error) {
	if s == nil || s.users == nil || q.BotID == 0 {
		return nil, 0, ErrInvalid
	}
	return s.users.ListTelegramUsers(q)
}
func (s *Service) CreateBroadcast(ctx context.Context, input CreateInput) (*broadcastdomain.Broadcast, error) {
	if s == nil || s.store == nil || s.deliveries == nil || input.BotID == 0 || strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.MessageHTML) == "" {
		return nil, ErrInvalid
	}
	if input.RecipientType != "all" && input.RecipientType != "specific" {
		return nil, ErrInvalid
	}
	if input.RecipientType == "specific" && len(input.UserIDs) == 0 {
		return nil, ErrNoRecipients
	}
	cipher, count, err := s.deliveries.Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNoRecipients
	}
	if cipher == "" {
		return nil, ErrInvalid
	}
	b := &broadcastdomain.Broadcast{
		BotID: input.BotID, DeliveryVersion: 1, PayloadCipher: cipher,
		Title: strings.TrimSpace(input.Title), RecipientType: input.RecipientType, RecipientCount: count, Status: "pending",
		MessageHTML: strings.TrimSpace(input.MessageHTML), AttachmentURL: strings.TrimSpace(input.AttachmentURL), AttachmentName: strings.TrimSpace(input.AttachmentName),
	}
	if err = s.store.Create(ctx, b); err != nil {
		return nil, err
	}
	// Enqueueing is durable and network-free. A crash partway through is repaired
	// by Reconcile using the same keys, never by restarting a send loop.
	if err = s.ProcessBroadcast(ctx, b.ID); err != nil {
		// Once persisted, return its stable ID even if recipient materialization
		// was interrupted. Recovery resumes this job; returning a create error
		// invites clients to create a second broadcast and double-send.
		return b, nil
	}
	if s.dispatcher != nil {
		_, _ = s.dispatcher.DispatchBroadcast(ctx, b.ID)
	}
	current, readErr := s.store.GetByID(ctx, b.ID)
	if readErr == nil && current != nil {
		return current, nil
	}
	return b, nil
}
func (s *Service) ProcessBroadcast(ctx context.Context, id uint) error {
	if s == nil || s.store == nil {
		return ErrInvalid
	}
	b, err := s.store.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if b == nil {
		return ErrNotFound
	}
	if b.Status == "completed" || (b.Status == "needs_review" && b.DeliveryVersion != 1) {
		return nil
	}
	if b.DeliveryVersion != 1 || b.BotID == 0 || b.PayloadCipher == "" {
		b.Status = "needs_review"
		b.LastError = "legacy broadcast has no recipient delivery ledger"
		return s.store.Update(ctx, b)
	}
	if s.deliveries == nil {
		return ErrInvalid
	}
	if err = s.deliveries.Enqueue(ctx, b); err != nil {
		return err
	}
	p, err := s.deliveries.Progress(ctx, b.BotID, b.ID)
	if err != nil {
		return err
	}
	b.SuccessCount = p.Sent
	b.FailedCount = p.Failed
	b.UnknownCount = p.Unknown
	now := time.Now()
	if b.StartedAt == nil {
		b.StartedAt = &now
	}
	b.Status = "running"
	b.CompletedAt = nil
	b.LastError = ""
	if p.Total == b.RecipientCount && p.Pending == 0 {
		b.CompletedAt = &now
		if p.Unknown > 0 {
			b.Status = "needs_review"
			b.LastError = "delivery outcome requires reconciliation"
		} else if p.Failed > 0 {
			b.Status = "failed"
		} else {
			b.Status = "completed"
		}
	}
	return s.store.Update(ctx, b)
}

// Reconcile must be wired into the supervised runtime recovery tick; it does
// not spawn goroutines and also quarantines untracked historical broadcasts.
func (s *Service) Reconcile(ctx context.Context) error {
	items, _, err := s.store.List(ctx, broadcastcontract.ListFilter{})
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Failed/review jobs are rechecked too: an explicit retry can race a
		// progress snapshot, so their delivery ledger remains the source of truth.
		if b.Status == "completed" || (b.Status == "needs_review" && b.DeliveryVersion != 1) {
			continue
		}
		if err := s.ProcessBroadcast(ctx, b.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
