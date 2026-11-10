package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/XManCodeing/XMall/internal/i18n"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"log/slog"
	"strings"
	"time"
)

type DeliveryStore interface {
	EnqueueRequest(context.Context, *r.DeliveryRequest) error
}
type Service struct {
	Store         c.Store
	Cipher        c.Cipher
	Modules       c.Modules
	DeliveryReady c.DeliveryCapability
	Deliveries    DeliveryStore
	Policy        c.Policy
	Policies      c.PolicyReader
	Now           func() time.Time
}

func New(store c.Store, cipher c.Cipher, modules c.Modules, deliveries DeliveryStore) *Service {
	return &Service{Store: store, Cipher: cipher, Modules: modules, Deliveries: deliveries, Policy: c.DefaultPolicy(), Now: time.Now}
}

// WithPolicies installs the trusted per-bot policy reader and returns s for composition.
func (s *Service) WithPolicies(policies c.PolicyReader) *Service { s.Policies = policies; return s }

// policyFor snapshots current configuration for one inbound update without mutating shared Service.Policy.
// Legacy/test assemblies retain their explicit Policy; production always installs persistent storage.
func (s *Service) policyFor(ctx context.Context, bot uint) (c.Policy, error) {
	policy := s.Policy
	if s.Policies != nil {
		var err error
		policy, _, err = s.Policies.Policy(ctx, bot)
		if err != nil {
			return c.Policy{}, err
		}
	}
	if !policy.Valid() {
		return c.Policy{}, c.ErrPolicyInvalid
	}
	return c.VerificationPolicy(policy), nil
}

func (s *Service) ReadyEnabled(ctx context.Context, bot uint) (bool, error) {
	if bot == 0 || s.DeliveryReady == nil || s.Modules == nil {
		return false, nil
	}
	if _, err := s.policyFor(ctx, bot); err != nil {
		return false, err
	}
	modules, err := s.Modules.Modules(ctx, bot)
	if err != nil {
		return false, err
	}
	for _, m := range modules {
		if m.Partition == "support" && m.Enabled && m.EffectiveEnabled && m.TargetID != 0 {
			return s.DeliveryReady.Ready(ctx, bot, m.TargetID)
		}
	}
	return false, nil
}
func private(u r.Update) bool {
	return u.Private && u.UserID > 0 && u.ChatID == u.UserID && (u.ChatType == "private" || u.ChatType == "") && u.SenderChat == nil
}
func (s *Service) Handle(ctx context.Context, bot uint, u r.Update) ([]r.Message, error) {
	if !private(u) || bot == 0 || u.ID <= 0 {
		return nil, nil
	}
	enabled, err := s.ReadyEnabled(ctx, bot)
	if err != nil || !enabled {
		return nil, err
	}
	policy, err := s.policyFor(ctx, bot)
	if err != nil {
		return nil, err
	}
	now := s.Now().UTC()
	if strings.HasPrefix(u.CallbackData, "support:") {
		if u.CallbackID == "" || (u.Type != "" && u.Type != "callback_query") {
			return nil, nil
		}
		ref := strings.TrimPrefix(u.CallbackData, "support:")
		outcome, err := s.Store.Verify(ctx, bot, u, ref, policy, now)
		if err != nil {
			if errors.Is(err, c.ErrDenied) {
				r.AnswerCallback(ctx, feedback(u.Locale, "invalid"))
				return nil, nil
			}
			return nil, err
		}
		slog.InfoContext(ctx, "support_verification_result", "bot_id", bot, "update_id", u.ID, "session_id", outcome.Session.ID, "reason", outcome.Reason)
		r.AnswerCallback(ctx, feedback(u.Locale, outcome.Reason))
		if outcome.Reason == "passed" {
			return nil, s.enqueueVerificationCleanup(ctx, bot, u)
		}
		return nil, nil
	}
	if u.CallbackID != "" || u.CallbackData != "" || (u.Type != "" && u.Type != "message") || u.MessageID <= 0 || strings.HasPrefix(strings.TrimSpace(u.Text), "/") || (u.Text == "" && len(u.Media) == 0 && (u.MessageKind == "" || u.MessageKind == "service" || u.MessageKind == "migration")) {
		return nil, nil
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.Cipher.Seal(string(raw))
	if err != nil {
		return nil, err
	}
	token := make([]byte, 18)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	_, err = s.Store.Accept(ctx, bot, u, encrypted, int64(len(raw)), base64.RawURLEncoding.EncodeToString(token), policy, now)
	if errors.Is(err, c.ErrDenied) {
		// Invalid/replayed input cannot become valid by retrying the inbox.
		slog.WarnContext(ctx, "support_input_denied", "bot_id", bot)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Intake is already durable. Notification repair belongs to the supervised
	// worker; an unrelated bad notice must never retry this serial inbox entry.
	repairCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.Reconcile(repairCtx, bot); err != nil {
		slog.WarnContext(ctx, "support_notice_reconcile_deferred", "bot_id", bot, "update_id", u.ID)
	}
	return nil, nil
}
func (s *Service) enqueue(ctx context.Context, bot uint, u r.Update, ref, key, reason string, now time.Time) error {
	notice, err := s.Store.Notice(ctx, bot, ref)
	if err != nil {
		return err
	}
	m, err := s.noticeMessage(ctx, bot, notice)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	payload, err := s.Cipher.Seal(string(raw))
	if err != nil {
		return err
	}
	return s.Deliveries.EnqueueRequest(ctx, &r.DeliveryRequest{BotID: bot, Key: key, ChatID: u.ChatID, TelegramUserID: u.UserID, TargetType: string(r.TargetSupportPrivate), PayloadCipher: payload, Priority: r.PrioritySupport, Purpose: "support_gate", Operation: "send", Status: "pending", AvailableAt: now})
}
func (s *Service) Reconcile(ctx context.Context, bot uint) error {
	now := s.Now().UTC()
	if err := s.Store.Expire(ctx, bot, now); err != nil {
		return err
	}
	notices, err := s.Store.Notices(ctx, bot, 50, now)
	if err != nil {
		return err
	}
	var failures []error
	for _, n := range notices {
		if err = s.Store.CheckNotice(ctx, n, now); err != nil {
			if !errors.Is(err, c.ErrDenied) {
				failures = append(failures, err, s.deferNotice(ctx, n, now))
				continue
			}
			// A deterministic block consumes the pending notice; it must not
			// loop forever or become deliverable after a later unblock.
			if err = s.Store.NoticeEnqueued(ctx, n); err != nil {
				failures = append(failures, err, s.deferNotice(ctx, n, now))
				continue
			}
			slog.InfoContext(ctx, "support_notice_suppressed", "bot_id", bot, "kind", n.Kind)
			continue
		}
		err = s.enqueue(ctx, bot, r.Update{UserID: n.UserID, ChatID: n.ChatID, Locale: n.Locale}, n.Ref, n.Key, n.Reason, now)
		if err != nil {
			// Keep failed notices durable and visible, but never block later customers.
			// Deterministically invalid notices are quarantined as pending=false with
			// their original reason and error metadata retained for administrator review.
			if errors.Is(err, c.ErrDenied) {
				if quarantine, ok := s.Store.(c.NoticeQuarantine); ok {
					if quarantineErr := quarantine.QuarantineNotice(ctx, n); quarantineErr != nil {
						failures = append(failures, quarantineErr)
					}
				}
			}
			slog.WarnContext(ctx, "support_notice_failed", "bot_id", bot, "notice_id", n.ID, "kind", n.Kind)
			failures = append(failures, err, s.deferNotice(ctx, n, now))
			continue
		}
		if err = s.Store.NoticeEnqueued(ctx, n); err != nil {
			failures = append(failures, err, s.deferNotice(ctx, n, now))
		}
	}
	return errors.Join(failures...)
}

// deferNotice rotates a failed row when supported; the worker retains its error.
// n is bot-scoped durable metadata, and now is the reconciliation clock.
func (s *Service) deferNotice(ctx context.Context, n c.Notice, now time.Time) error {
	if store, ok := s.Store.(c.NoticeDeferral); ok {
		return store.DeferNotice(ctx, n, now)
	}
	return nil
}

func feedback(locale, reason string) string {
	if strings.HasPrefix(locale, "en") {
		locale = "en-US"
	} else if locale == "zh-Hant" {
		locale = "zh-TW"
	} else if locale != "zh-TW" {
		locale = "zh-CN"
	}
	return i18n.T(locale, "support."+reason)
}
