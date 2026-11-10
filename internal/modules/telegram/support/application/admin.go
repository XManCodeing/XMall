package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"time"

	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// AdminService exposes authenticated read operations independently of the production consumer.
// All dependencies are mandatory; authorization is repeated at the application boundary.
type AdminService struct {
	deliveryRecovery  c.AdminDeliveryRecoverer
	authorizeRecovery c.AdminRecoveryAuthorize
	recovery          c.AdminRecoveryStore
	recoveryResolver  *Resolver
	readiness         func(context.Context, uint) (bool, error)
	policies          c.PolicyStore
	authorizeSettings c.AdminSettingsAuthorize
	tasks             c.AdminDownstreamStore
	resolver          *DownstreamResolver
	generation        c.AdminGeneration
	store             c.AdminReadStore
	cipher            c.Cipher
	authorize         c.AdminAuthorize
	audit             c.AdminAudit
	now               func() time.Time
}

// NewAdmin constructs the read service from trusted backend authorization and audit adapters.
// It returns a fail-closed service even when a dependency is unavailable.
func NewAdmin(store c.AdminReadStore, cipher c.Cipher, authorize c.AdminAuthorize, audit c.AdminAudit) *AdminService {
	return &AdminService{store: store, cipher: cipher, authorize: authorize, audit: audit, now: time.Now}
}

// WithSettings binds policy persistence and independent configuration permissions; returns s.
func (s *AdminService) WithSettings(store c.PolicyStore, authorize c.AdminSettingsAuthorize) *AdminService {
	s.policies, s.authorizeSettings = store, authorize
	return s
}

// settingsAccess checks the backend actor and bot before any policy read/write.
func (s *AdminService) settingsAccess(ctx context.Context, actor, bot uint, method string) error {
	if actor == 0 {
		return c.ErrDenied
	}
	if bot == 0 {
		return c.ErrAdminInvalid
	}
	if s == nil || s.policies == nil || s.authorizeSettings == nil {
		return c.ErrAdminUnavailable
	}
	return s.authorizeSettings(ctx, actor, bot, method)
}

// WithReadiness installs the same readiness reader used by private ingress.
// A nil reader retains the fail-closed default; returns s for composition.
func (s *AdminService) WithReadiness(reader func(context.Context, uint) (bool, error)) *AdminService {
	s.readiness = reader
	return s
}

// settingsView returns policy metadata and current production readiness.
// The optional reader is invoked outside policy transactions; errors remain errors.
func (s *AdminService) settingsView(ctx context.Context, bot uint, policy c.Policy, version uint64) (c.AdminSettings, error) {
	out := c.AdminSettings{BotID: bot, Version: version, UsingDefaults: version == 0, Policy: c.PolicyDTO(policy), Defaults: c.PolicyDTO(c.DefaultPolicy()), VerificationMode: "click_required"}
	var err error
	if s.readiness != nil {
		out.ProductionReady, err = s.readiness(ctx, bot)
	}
	return out, err
}

// Settings returns this bot's override or technical defaults; read failures remain failures.
func (s *AdminService) Settings(ctx context.Context, actor, bot uint) (c.AdminSettings, error) {
	if err := s.settingsAccess(ctx, actor, bot, "GET"); err != nil {
		return c.AdminSettings{}, err
	}
	policy, version, err := s.policies.Policy(ctx, bot)
	if err != nil {
		return c.AdminSettings{}, err
	}
	return s.settingsView(ctx, bot, policy, version)
}

// UpdateSettings replaces one bot's policy with CAS, never rewrites historical challenge/message rows.
func (s *AdminService) UpdateSettings(ctx context.Context, actor, bot uint, in c.AdminSettingsInput) (c.AdminSettings, error) {
	if err := s.settingsAccess(ctx, actor, bot, "PUT"); err != nil {
		return c.AdminSettings{}, err
	}
	if in.ExpectedVersion == nil || *in.ExpectedVersion >= uint64(1<<63-1) {
		return c.AdminSettings{}, c.ErrPolicyInvalid
	}
	policy, err := in.Policy.Policy()
	if err != nil {
		return c.AdminSettings{}, err
	}
	version, err := s.policies.SavePolicy(ctx, bot, actor, *in.ExpectedVersion, policy, s.now().UTC())
	if err != nil {
		slog.Warn("support_policy_update_rejected", "admin_id", actor, "bot_id", bot)
		return c.AdminSettings{}, err
	}
	slog.Info("support_policy_updated", "admin_id", actor, "bot_id", bot, "version", version)
	return s.settingsView(ctx, bot, policy, version)
}

// WithDownstreams installs trusted task adapters during container construction and returns s.
func (s *AdminService) WithDownstreams(tasks c.AdminDownstreamStore, resolver *DownstreamResolver, generation c.AdminGeneration) *AdminService {
	s.tasks, s.resolver, s.generation = tasks, resolver, generation
	return s
}

// Downstreams returns bounded metadata for an authenticated backend actor; unknown is read-only here.
func (s *AdminService) Downstreams(ctx context.Context, actor uint, filter c.AdminFilter) (c.AdminPage[c.AdminDownstream], error) {
	out := c.AdminPage[c.AdminDownstream]{Items: []c.AdminDownstream{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.check(ctx, actor, c.AdminDownstreamsPath, false); err != nil {
		return out, err
	}
	if !validFilter(filter) {
		return out, c.ErrAdminInvalid
	}
	if s.tasks == nil {
		return out, c.ErrAdminUnavailable
	}
	rows, total, err := s.tasks.Downstreams(ctx, filter)
	if err != nil {
		return out, err
	}
	if rows != nil {
		out.Items = rows
	}
	out.Total = total
	return out, nil
}

// Downstream reads one metadata-only task; authorization does not imply resolution permission.
func (s *AdminService) Downstream(ctx context.Context, actor, id uint) (c.AdminDownstream, error) {
	if err := s.check(ctx, actor, c.AdminDownstreamPath, false); err != nil {
		return c.AdminDownstream{}, err
	}
	if id == 0 {
		return c.AdminDownstream{}, c.ErrAdminInvalid
	}
	if s.tasks == nil {
		return c.AdminDownstream{}, c.ErrAdminUnavailable
	}
	return s.tasks.Downstream(ctx, id)
}

// Evidence is a ticket/audit reference, not arbitrary notes, URLs, tokens or message bodies.
var adminEvidenceReference = regexp.MustCompile(`^(ticket|audit|case):[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// ResolveDownstream authorizes a backend actor before loading trusted generation and invoking
// the real resolver. It neither sends nor manufactures a runtime receipt; storage validates proof.
func (s *AdminService) ResolveDownstream(ctx context.Context, actor, id uint, in c.AdminResolutionInput) error {
	if err := s.check(ctx, actor, c.AdminDownstreamPath, false); err != nil {
		return err
	}
	if s.tasks == nil || s.resolver == nil || s.resolver.Authorizer == nil || s.generation == nil {
		return c.ErrAdminUnavailable
	}
	if id == 0 || in.ExpectedVersion == 0 || in.ExpectedVersion == ^uint64(0) || in.DeliveryID == 0 || !adminEvidenceReference.MatchString(in.Evidence) || (in.Decision != "delivered" && in.Decision != "failed") {
		return c.ErrAdminInvalid
	}
	// The bot comes from the task, never from a client-supplied trusted routing field.
	row, err := s.tasks.Downstream(ctx, id)
	if err != nil {
		return err
	}
	if err = s.resolver.Authorizer.AuthorizeSupportResolution(ctx, actor, row.BotID); err != nil {
		return err
	}
	generation, err := s.generation(ctx, row.BotID)
	if err != nil {
		return err
	}
	if generation == 0 {
		return c.ErrConflict
	}
	err = s.resolver.Resolve(ctx, row.BotID, id, generation, c.Resolution{ActorID: actor, ExpectedVersion: in.ExpectedVersion, Decision: in.Decision, Evidence: in.Evidence, DeliveryID: in.DeliveryID}, s.now().UTC())
	if err != nil {
		slog.Warn("support_downstream_resolution_rejected", "admin_id", actor, "task_id", id)
		return err
	}
	slog.Info("support_downstream_resolution_recorded", "admin_id", actor, "task_id", id)
	return nil
}

// WithRecovery binds independent topic/upstream queries and the existing authorized resolver.
func (s *AdminService) WithRecovery(store c.AdminRecoveryStore, resolver *Resolver, generation c.AdminGeneration) *AdminService {
	s.recovery, s.recoveryResolver, s.generation = store, resolver, generation
	return s
}

// recoveryFilter requires an explicit bot and a resource-specific finite state.
func recoveryFilter(filter c.AdminFilter, topic bool) bool {
	if filter.BotID == 0 || filter.Page < 1 || filter.Page > 1000000 || filter.PageSize < 1 || filter.PageSize > 100 {
		return false
	}
	if topic {
		return filter.State == "" || filter.State == "creating" || filter.State == "unknown" || filter.State == "succeeded" || filter.State == "failed"
	}
	switch filter.State {
	case "", "buffered", "ready", "claimed", "sending", "unknown", "delivered", "failed", "expired", "rejected", "cancelled":
		return true
	default:
		return false
	}
}

// TopicOperations returns per-bot operation metadata without claims or Telegram body data.
func (s *AdminService) TopicOperations(ctx context.Context, actor uint, filter c.AdminFilter) (c.AdminPage[c.AdminTopicOperation], error) {
	out := c.AdminPage[c.AdminTopicOperation]{Items: []c.AdminTopicOperation{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.check(ctx, actor, c.AdminTopicsPath, false); err != nil {
		return out, err
	}
	if !recoveryFilter(filter, true) {
		return out, c.ErrAdminInvalid
	}
	if s.recovery == nil {
		return out, c.ErrAdminUnavailable
	}
	rows, total, err := s.recovery.TopicOperations(ctx, filter)
	if rows != nil {
		out.Items = rows
	}
	out.Total = total
	return out, err
}

// TopicOperation resolves only the topic-operation namespace for a backend actor.
func (s *AdminService) TopicOperation(ctx context.Context, actor, id uint) (c.AdminTopicOperation, error) {
	if err := s.check(ctx, actor, c.AdminTopicPath, false); err != nil {
		return c.AdminTopicOperation{}, err
	}
	if id == 0 {
		return c.AdminTopicOperation{}, c.ErrAdminInvalid
	}
	if s.recovery == nil {
		return c.AdminTopicOperation{}, c.ErrAdminUnavailable
	}
	return s.recovery.TopicOperation(ctx, id)
}

// Upstreams returns buffered-message metadata and the independent dispatch CAS version.
func (s *AdminService) Upstreams(ctx context.Context, actor uint, filter c.AdminFilter) (c.AdminPage[c.AdminUpstream], error) {
	out := c.AdminPage[c.AdminUpstream]{Items: []c.AdminUpstream{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.check(ctx, actor, c.AdminUpstreamsPath, false); err != nil {
		return out, err
	}
	if !recoveryFilter(filter, false) {
		return out, c.ErrAdminInvalid
	}
	if s.recovery == nil {
		return out, c.ErrAdminUnavailable
	}
	rows, total, err := s.recovery.Upstreams(ctx, filter)
	if rows != nil {
		out.Items = rows
	}
	out.Total = total
	return out, err
}

// Upstream reads a buffered-message ID; it never interprets it as a downstream ID.
func (s *AdminService) Upstream(ctx context.Context, actor, id uint) (c.AdminUpstream, error) {
	if err := s.check(ctx, actor, c.AdminUpstreamPath, false); err != nil {
		return c.AdminUpstream{}, err
	}
	if id == 0 {
		return c.AdminUpstream{}, c.ErrAdminInvalid
	}
	if s.recovery == nil {
		return c.AdminUpstream{}, c.ErrAdminUnavailable
	}
	return s.recovery.Upstream(ctx, id)
}

// recoveryGeneration checks independent resolution authority before reading trusted runtime state.
func (s *AdminService) recoveryGeneration(ctx context.Context, actor, bot uint) (uint64, error) {
	if s.recoveryResolver == nil || s.recoveryResolver.Authorizer == nil || s.generation == nil {
		return 0, c.ErrAdminUnavailable
	}
	if err := s.recoveryResolver.Authorizer.AuthorizeSupportResolution(ctx, actor, bot); err != nil {
		return 0, err
	}
	generation, err := s.generation(ctx, bot)
	if err == nil && generation == 0 {
		err = c.ErrConflict
	}
	return generation, err
}

// ResolveTopicOperation records a deliberate CAS decision; failed explicitly means proven no effect.
func (s *AdminService) ResolveTopicOperation(ctx context.Context, actor, id uint, in c.AdminTopicResolutionInput) error {
	row, err := s.TopicOperation(ctx, actor, id)
	if err != nil {
		return err
	}
	if in.ExpectedVersion == 0 || in.ExpectedVersion >= uint64(1<<63-1) || !adminEvidenceReference.MatchString(in.Evidence) {
		return c.ErrAdminInvalid
	}
	if (in.Decision == "succeeded" && (in.ThreadID <= 0 || in.NoEffect)) || (in.Decision == "failed" && (!in.NoEffect || in.ThreadID != 0)) || (in.Decision != "succeeded" && in.Decision != "failed") {
		return c.ErrAdminInvalid
	}
	generation, err := s.recoveryGeneration(ctx, actor, row.BotID)
	if err != nil {
		return err
	}
	err = s.recoveryResolver.ResolveTopic(ctx, row.BotID, id, c.Resolution{Generation: generation, ActorID: actor, ExpectedVersion: in.ExpectedVersion, Decision: in.Decision, Evidence: in.Evidence, ThreadID: in.ThreadID}, s.now().UTC())
	logRecovery(actor, id, "topic", err)
	return err
}

// ResolveUpstream validates runtime proof through ResolveReady; clients cannot supply Telegram IDs.
func (s *AdminService) ResolveUpstream(ctx context.Context, actor, id uint, in c.AdminResolutionInput) error {
	row, err := s.Upstream(ctx, actor, id)
	if err != nil {
		return err
	}
	if in.ExpectedVersion == 0 || in.ExpectedVersion >= uint64(1<<63-1) || in.DeliveryID == 0 || !adminEvidenceReference.MatchString(in.Evidence) || (in.Decision != "delivered" && in.Decision != "failed") {
		return c.ErrAdminInvalid
	}
	generation, err := s.recoveryGeneration(ctx, actor, row.BotID)
	if err != nil {
		return err
	}
	err = s.recoveryResolver.ResolveReady(ctx, row.BotID, id, generation, c.Resolution{ActorID: actor, ExpectedVersion: in.ExpectedVersion, Decision: in.Decision, Evidence: in.Evidence, DeliveryID: in.DeliveryID}, s.now().UTC())
	logRecovery(actor, id, "upstream", err)
	return err
}

// logRecovery logs local identifiers only, never evidence, transport errors or message contents.
func logRecovery(actor, id uint, resource string, err error) {
	if err != nil {
		slog.Warn("support_resolution_rejected", "admin_id", actor, "resource", resource, "id", id)
		return
	}
	slog.Info("support_resolution_recorded", "admin_id", actor, "resource", resource, "id", id)
}

// check requires a positive backend actor and validates the specific route permission.
func (s *AdminService) check(ctx context.Context, actor uint, path string, exact bool) error {
	if actor == 0 {
		return c.ErrDenied
	}
	if s == nil || s.authorize == nil || s.store == nil {
		return c.ErrAdminUnavailable
	}
	return s.authorize(ctx, actor, path, exact)
}

// validFilter validates pagination before offset calculation and accepts only finite states.
func validFilter(filter c.AdminFilter) bool {
	if filter.Page < 1 || filter.Page > 1000000 || filter.PageSize < 1 || filter.PageSize > 100 {
		return false
	}
	switch filter.State {
	case "", "collecting", "cancelled", "internal", "waiting_verification", "callback", "pending", "challenged", "verified", "active", "open", "closed", "cooldown", "buffered", "ready", "claimed", "sending", "unknown", "delivered", "failed", "expired", "rejected":
		return true
	default:
		return false
	}
}

// Sessions returns a bounded metadata page for an authorized backend actor.
func (s *AdminService) Sessions(ctx context.Context, actor uint, filter c.AdminFilter) (c.AdminPage[c.AdminSession], error) {
	out := c.AdminPage[c.AdminSession]{Items: []c.AdminSession{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.check(ctx, actor, c.AdminSessionsPath, false); err != nil {
		return out, err
	}
	if !validFilter(filter) || !filter.ValidSessionOperations() {
		return out, c.ErrAdminInvalid
	}
	rows, total, err := s.store.Sessions(ctx, filter)
	if err != nil {
		return out, err
	}
	if rows != nil {
		out.Items = rows
	}
	out.Total = total
	return out, nil
}

// Session returns one metadata record without reading any buffered payload.
func (s *AdminService) Session(ctx context.Context, actor, session uint) (c.AdminSession, error) {
	if err := s.check(ctx, actor, c.AdminSessionPath, false); err != nil {
		return c.AdminSession{}, err
	}
	if session == 0 {
		return c.AdminSession{}, c.ErrAdminInvalid
	}
	return s.store.Session(ctx, session)
}

// Messages returns metadata for messages belonging to an existing session.
func (s *AdminService) Messages(ctx context.Context, actor, session uint, filter c.AdminFilter) (c.AdminPage[c.AdminMessage], error) {
	out := c.AdminPage[c.AdminMessage]{Items: []c.AdminMessage{}, Page: filter.Page, PageSize: filter.PageSize}
	if err := s.check(ctx, actor, c.AdminMessagesPath, false); err != nil {
		return out, err
	}
	if session == 0 || !validFilter(filter) {
		return out, c.ErrAdminInvalid
	}
	if _, err := s.store.Session(ctx, session); err != nil {
		return out, err
	}
	rows, total, err := s.store.Messages(ctx, session, filter)
	if err != nil {
		return out, err
	}
	if rows != nil {
		out.Items = rows
	}
	out.Total = total
	return out, nil
}

// Content requires an exact sensitive permission and durable audit before returning display text.
// Expiration and cleared payloads are successful queries with explicit unavailability, not retries.
func (s *AdminService) Content(ctx context.Context, actor, session, message uint) (c.AdminContent, error) {
	if err := s.check(ctx, actor, c.AdminContentPath, true); err != nil {
		return c.AdminContent{}, err
	}
	if session == 0 || message == 0 {
		return c.AdminContent{}, c.ErrAdminInvalid
	}
	if s.cipher == nil || s.audit == nil {
		return c.AdminContent{}, c.ErrAdminUnavailable
	}
	payload, err := s.store.Payload(ctx, session, message)
	if err != nil {
		return c.AdminContent{}, err
	}
	out := c.AdminContent{}
	switch {
	case !s.now().Before(payload.RetainUntil):
		out.Reason = "expired"
	case payload.Cipher == "":
		out.Reason = "cleared"
	default:
		out, err = s.decode(payload.Cipher)
	}
	outcome := out.Reason
	if err != nil {
		outcome = "invalid_payload"
	}
	if out.Available {
		outcome = "viewed"
	}
	// Audit receives no raw errors or user-controlled content and fails closed.
	if auditErr := s.audit(ctx, actor, session, message, outcome); auditErr != nil {
		slog.Error("support_content_audit_failed", "admin_id", actor, "session_id", session, "message_id", message)
		return c.AdminContent{}, c.ErrAdminUnavailable
	}
	if err != nil {
		slog.Warn("support_content_decode_failed", "admin_id", actor, "session_id", session, "message_id", message)
		return c.AdminContent{}, c.ErrAdminUnavailable
	}
	// A slow audit cannot authorize text beyond its retention boundary.
	if out.Available && !s.now().Before(payload.RetainUntil) {
		return c.AdminContent{Reason: "expired"}, nil
	}
	return out, nil
}

// decode projects a decrypted runtime update into display-only fields, excluding entities and file IDs.
func (s *AdminService) decode(cipher string) (c.AdminContent, error) {
	plain, err := s.cipher.Open(cipher)
	if err != nil {
		return c.AdminContent{}, err
	}
	var update r.Update
	if err = json.Unmarshal([]byte(plain), &update); err != nil {
		return c.AdminContent{}, err
	}
	out := c.AdminContent{Available: true, Kind: displayKind(update.MessageKind), Text: update.Text, Caption: update.Caption}
	if out.Kind == "unknown" && len(update.Media) == 0 && update.Text != "" {
		out.Kind = "text"
	}
	for _, media := range update.Media {
		out.MediaKinds = append(out.MediaKinds, displayKind(media.Type))
	}
	return out, nil
}

// displayKind returns only a finite presentation type, never a transport-controlled string.
func displayKind(kind string) string {
	switch kind {
	case "text", "photo", "video", "voice", "audio", "document", "animation", "sticker", "album":
		return kind
	default:
		return "unknown"
	}
}
