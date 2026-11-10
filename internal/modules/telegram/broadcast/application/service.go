package broadcastapp

import (
	"context"
	"errors"
	"strings"
	"sync"
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
type BroadcastContent struct {
	MessageHTML    string     `json:"message_html"`
	AttachmentURL  string     `json:"attachment_url"`
	AttachmentName string     `json:"attachment_name"`
	Buttons        [][]Button `json:"buttons"`
}

// BroadcastDetail is a response-only copy; never pass it to Store.Update.
type BroadcastDetail struct {
	broadcastdomain.Broadcast
	Buttons [][]Button `json:"buttons"`
}

type ContentReader interface {
	ReadContent(string) (BroadcastContent, error)
}

type PreviewResult struct {
	RecipientCount int `json:"recipient_count"`
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
	store          broadcastcontract.Store
	users          UserDirectory
	deliveries     DeliveryBridge
	dispatcher     Dispatcher
	reconcileMu    sync.Mutex
	reconcileAfter uint
}

func NewService(store broadcastcontract.Store, users UserDirectory, _ BotTokenResolver, dispatcher Dispatcher, _ Sender) *Service {
	return &Service{store: store, users: users, dispatcher: dispatcher}
}
func NewRuntimeService(store broadcastcontract.Store, users UserDirectory, deliveries DeliveryBridge, dispatcher Dispatcher) *Service {
	return &Service{store: store, users: users, deliveries: deliveries, dispatcher: dispatcher}
}
func (s *Service) GetBroadcast(ctx context.Context, id uint) (*BroadcastDetail, error) {
	b, err := s.store.GetByID(ctx, id)
	if err != nil || b == nil {
		return nil, err
	}
	detail := &BroadcastDetail{Broadcast: *b, Buttons: [][]Button{}}
	if b.PayloadCipher == "" {
		// Only unversioned historical rows may use the legacy display fields.
		if b.DeliveryVersion != 0 {
			return nil, ErrInvalid
		}
		return detail, nil
	}
	reader, ok := s.deliveries.(ContentReader)
	if !ok {
		return nil, ErrInvalid
	}
	content, err := reader.ReadContent(b.PayloadCipher)
	if err != nil {
		// Never expose decrypt errors or fall back to stale plaintext.
		return nil, ErrInvalid
	}
	detail.MessageHTML = content.MessageHTML
	detail.AttachmentURL = content.AttachmentURL
	detail.AttachmentName = content.AttachmentName
	if content.Buttons != nil {
		detail.Buttons = content.Buttons
	}
	return detail, nil
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
func (s *Service) PreviewBroadcast(ctx context.Context, input CreateInput) (PreviewResult, error) {
	if s == nil || s.deliveries == nil || input.BotID == 0 || strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.MessageHTML) == "" || (input.RecipientType != "all" && input.RecipientType != "specific") {
		return PreviewResult{}, ErrInvalid
	}
	if err := ValidateMessageHTML(input.MessageHTML); err != nil {
		return PreviewResult{}, err
	}
	// Prepare is read-only: it validates and snapshots the exact create audience.
	// The temporary encrypted snapshot is discarded; create always prepares anew.
	_, count, err := s.deliveries.Prepare(ctx, input)
	return PreviewResult{RecipientCount: count}, err
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
	if err := ValidateMessageHTML(input.MessageHTML); err != nil {
		return nil, err
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
	// A complete recipient ledger never needs materializing again. Partial
	// preparation remains idempotent and resumes only missing delivery rows.
	p, err := s.deliveries.Progress(ctx, b.BotID, b.ID)
	if err != nil {
		return err
	}
	if p.Total < b.RecipientCount {
		if err = s.deliveries.Enqueue(ctx, b); err != nil {
			return err
		}
		p, err = s.deliveries.Progress(ctx, b.BotID, b.ID)
		if err != nil {
			return err
		}
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

// Reconcile visits one bounded keyset page of active jobs per supervised tick.
// Explicit delivery retry atomically reopens failed/review jobs; terminal history
// needs no polling. The cursor prevents one broken active job starving later jobs.
func (s *Service) Reconcile(ctx context.Context) error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	const pageSize = 50
	items, _, err := s.store.List(ctx, broadcastcontract.ListFilter{PageSize: pageSize, ActiveOnly: true, Keyset: true, AfterID: s.reconcileAfter})
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.reconcileAfter = b.ID
		if b.Status != "pending" && b.Status != "running" {
			continue
		}
		if err := s.ProcessBroadcast(ctx, b.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(items) < pageSize {
		s.reconcileAfter = 0
	}
	return errors.Join(errs...)
}
