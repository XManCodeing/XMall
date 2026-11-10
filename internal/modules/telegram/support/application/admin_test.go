package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

type failingPolicyReader struct{}

// Policy models unavailable persistence: callers must propagate errors instead of accepting defaults.
func (failingPolicyReader) Policy(context.Context, uint) (c.Policy, uint64, error) {
	return c.Policy{}, 0, c.ErrAdminUnavailable
}

// TestRuntimePolicyFailureFailsClosed checks that gate-off does not read persistence and gate-on does.
func TestRuntimePolicyFailureFailsClosed(t *testing.T) {
	service := New(nil, nil, nil, nil).WithPolicies(failingPolicyReader{})
	if enabled, err := service.ReadyEnabled(context.Background(), 1); err != nil || enabled {
		t.Fatal(enabled, err)
	}
	if _, err := service.policyFor(context.Background(), 1); !errors.Is(err, c.ErrAdminUnavailable) {
		t.Fatal(err)
	}
}

type adminPolicyFixture struct {
	c.PolicyStore
	writes  int
	policy  c.Policy
	version uint64
}

func (f *adminPolicyFixture) Policy(context.Context, uint) (c.Policy, uint64, error) {
	if f.version == 0 {
		return c.DefaultPolicy(), 0, nil
	}
	return f.policy, f.version, nil
}
func (f *adminPolicyFixture) SavePolicy(_ context.Context, bot, actor uint, expected uint64, policy c.Policy, _ time.Time) (uint64, error) {
	if expected != f.version {
		return 0, c.ErrConflict
	}
	f.writes++
	f.version++
	f.policy = policy
	return f.version, nil
}

// TestAdminSettingsCASValidationAndGate verifies complete DTO validation and independent method authorization.
func TestAdminSettingsCASValidationAndGate(t *testing.T) {
	fixture := &adminPolicyFixture{}
	denied := false
	methods := []string{}
	service := NewAdmin(nil, nil, nil, nil).WithSettings(fixture, func(_ context.Context, actor, bot uint, method string) error {
		methods = append(methods, method)
		if denied || actor != 9 || bot != 2 {
			return c.ErrDenied
		}
		return nil
	})
	ctx := context.Background()
	out, err := service.Settings(ctx, 9, 2)
	if err != nil || out.Version != 0 || !out.UsingDefaults || out.ProductionReady || out.VerificationMode != "click_required" {
		t.Fatal(out, err)
	}
	version := uint64(0)
	input := c.AdminSettingsInput{ExpectedVersion: &version, Policy: c.PolicyDTO(c.DefaultPolicy())}
	out, err = service.UpdateSettings(ctx, 9, 2, input)
	if err != nil || out.Version != 1 || out.UsingDefaults || out.ProductionReady {
		t.Fatal(out, err)
	}
	if _, err = service.UpdateSettings(ctx, 9, 2, input); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*c.AdminSettingsInput){
		func(in *c.AdminSettingsInput) { in.ExpectedVersion = nil }, func(in *c.AdminSettingsInput) { in.Policy.MaxMessages = 101 }, func(in *c.AdminSettingsInput) { in.Policy.MaxBytes = 1023 }, func(in *c.AdminSettingsInput) { in.Policy.MaxAttempts = 0 }, func(in *c.AdminSettingsInput) { in.Policy.RetentionSeconds = 60; in.Policy.ChallengeTTLSeconds = 61 }, func(in *c.AdminSettingsInput) { in.Policy.CooldownSeconds = 3601 }, func(in *c.AdminSettingsInput) { in.Policy.RetentionSeconds = 1<<63 - 1 },
	} {
		bad := input
		mutate(&bad)
		if _, err = service.UpdateSettings(ctx, 9, 2, bad); !errors.Is(err, c.ErrPolicyInvalid) {
			t.Fatal(err)
		}
	}
	denied = true
	if _, err = service.Settings(ctx, 9, 2); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	if _, err = service.UpdateSettings(ctx, 9, 2, input); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	if fixture.writes != 1 || methods[0] != "GET" || methods[1] != "PUT" {
		t.Fatal(fixture, methods)
	}
}

// TestAdminSettingsLiveReadiness verifies unchanged DTOs reflect runtime state and errors.
func TestAdminSettingsLiveReadiness(t *testing.T) {
	fixture := &adminPolicyFixture{}
	service := NewAdmin(nil, nil, nil, nil).WithSettings(fixture, func(context.Context, uint, uint, string) error { return nil })
	ready := true
	var readErr error
	service.WithReadiness(func(_ context.Context, bot uint) (bool, error) {
		if bot != 2 {
			t.Fatal(bot)
		}
		return ready, readErr
	})
	ctx := context.Background()
	out, err := service.Settings(ctx, 9, 2)
	if err != nil || !out.ProductionReady || out.VerificationMode != "click_required" {
		t.Fatal(out, err)
	}
	version := uint64(0)
	out, err = service.UpdateSettings(ctx, 9, 2, c.AdminSettingsInput{ExpectedVersion: &version, Policy: c.PolicyDTO(c.DefaultPolicy())})
	if err != nil || !out.ProductionReady || out.Version != 1 {
		t.Fatal(out, err)
	}
	ready = false
	out, err = service.Settings(ctx, 9, 2)
	if err != nil || out.ProductionReady {
		t.Fatal(out, err)
	}
	readErr = errors.New("readiness database unavailable")
	if _, err = service.Settings(ctx, 9, 2); !errors.Is(err, readErr) {
		t.Fatal(err)
	}
}

type adminResolutionFixture struct {
	denied     bool
	writes     int
	calls      int
	generation uint64
	input      c.Resolution
}

func (f *adminResolutionFixture) Downstreams(context.Context, c.AdminFilter) ([]c.AdminDownstream, int64, error) {
	return []c.AdminDownstream{{ID: 3, BotID: 2, State: "unknown", Version: 4}}, 1, nil
}
func (f *adminResolutionFixture) Downstream(context.Context, uint) (c.AdminDownstream, error) {
	return c.AdminDownstream{ID: 3, BotID: 2, State: "unknown", Version: 4}, nil
}
func (f *adminResolutionFixture) AuthorizeSupportResolution(_ context.Context, actor, bot uint) error {
	f.calls++
	if f.denied || actor != 9 || bot != 2 {
		return c.ErrDenied
	}
	return nil
}
func (f *adminResolutionFixture) ResolveDownstream(_ context.Context, bot, id uint, generation uint64, in c.Resolution, _ time.Time) error {
	f.writes++
	f.generation = generation
	f.input = in
	return nil
}

// TestAdminDownstreamUsesTrustedGeneration proves real resolver invocation and separate authorization.
func TestAdminDownstreamUsesTrustedGeneration(t *testing.T) {
	fixture := &adminResolutionFixture{}
	generationReads := 0
	service := NewAdmin(&adminTestStore{}, nil, func(context.Context, uint, string, bool) error { return nil }, nil).WithDownstreams(fixture, &DownstreamResolver{Store: fixture, Authorizer: fixture}, func(_ context.Context, bot uint) (uint64, error) {
		if bot != 2 {
			t.Fatal(bot)
		}
		generationReads++
		return 17, nil
	})
	input := c.AdminResolutionInput{ExpectedVersion: 4, Decision: "delivered", Evidence: "ticket:review-3", DeliveryID: 8}
	if err := service.ResolveDownstream(context.Background(), 9, 3, input); err != nil {
		t.Fatal(err)
	}
	if fixture.writes != 1 || fixture.calls != 2 || fixture.generation != 17 || fixture.input.ActorID != 9 || fixture.input.ThreadID != 0 || fixture.input.DeliveryID != 8 {
		t.Fatal(fixture)
	}
	fixture.denied = true
	if err := service.ResolveDownstream(context.Background(), 9, 3, input); !errors.Is(err, c.ErrDenied) {
		t.Fatal(err)
	}
	if fixture.writes != 1 || generationReads != 1 {
		t.Fatal("denied resolution reached runtime")
	}
	fixture.denied = false
	service.generation = func(context.Context, uint) (uint64, error) { return 0, c.ErrConflict }
	if err := service.ResolveDownstream(context.Background(), 9, 3, input); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	for _, evidence := range []string{"raw user body", "https://token.invalid", "ticket:"} {
		input.Evidence = evidence
		if err := service.ResolveDownstream(context.Background(), 9, 3, input); !errors.Is(err, c.ErrAdminInvalid) {
			t.Fatal(err)
		}
	}
	if fixture.writes != 1 {
		t.Fatal("invalid input changed state")
	}
	page, err := service.Downstreams(context.Background(), 9, c.AdminFilter{Page: 1, PageSize: 20, State: "unknown"})
	if err != nil || page.Total != 1 || page.Items[0].State != "unknown" {
		t.Fatal(page, err)
	}
}

type adminTestStore struct {
	payload c.AdminPayload
	err     error
	reads   int
}

func (s *adminTestStore) Sessions(context.Context, c.AdminFilter) ([]c.AdminSession, int64, error) {
	s.reads++
	return nil, 0, s.err
}
func (s *adminTestStore) Session(context.Context, uint) (c.AdminSession, error) {
	s.reads++
	return c.AdminSession{ID: 1}, s.err
}
func (s *adminTestStore) Messages(context.Context, uint, c.AdminFilter) ([]c.AdminMessage, int64, error) {
	s.reads++
	return nil, 0, s.err
}
func (s *adminTestStore) Payload(context.Context, uint, uint) (c.AdminPayload, error) {
	s.reads++
	return s.payload, s.err
}

type adminTestCipher struct{ calls int }

func (cipher *adminTestCipher) Seal(value string) (string, error) { return value, nil }
func (cipher *adminTestCipher) Open(value string) (string, error) { cipher.calls++; return value, nil }

// TestAdminAuthorizationFailsBeforeReads ensures Telegram/user IDs cannot bypass the backend boundary.
func TestAdminAuthorizationFailsBeforeReads(t *testing.T) {
	store := &adminTestStore{}
	service := NewAdmin(store, &adminTestCipher{}, func(context.Context, uint, string, bool) error { return c.ErrDenied }, nil)
	for _, actor := range []uint{0, 777} {
		if _, err := service.Sessions(context.Background(), actor, c.AdminFilter{Page: 1, PageSize: 20}); !errors.Is(err, c.ErrDenied) {
			t.Fatal(err)
		}
		if _, err := service.Session(context.Background(), actor, 1); !errors.Is(err, c.ErrDenied) {
			t.Fatal(err)
		}
		if _, err := service.Messages(context.Background(), actor, 1, c.AdminFilter{Page: 1, PageSize: 20}); !errors.Is(err, c.ErrDenied) {
			t.Fatal(err)
		}
		if _, err := service.Content(context.Background(), actor, 1, 1); !errors.Is(err, c.ErrDenied) {
			t.Fatal(err)
		}
	}
	if store.reads != 0 {
		t.Fatal("unauthorized read")
	}
}

// TestAdminContentRetentionProjectionAndAudit verifies allowlisting and fail-closed durable audit.
func TestAdminContentRetentionProjectionAndAudit(t *testing.T) {
	now := time.Now().UTC()
	store := &adminTestStore{payload: c.AdminPayload{Cipher: `{"text":"hello","caption":"caption","message_kind":"photo","callback_data":"secret-callback","media":[{"type":"photo","file_id":"secret-file"}],"chat_id":99}`, RetainUntil: now.Add(time.Hour)}}
	cipher := &adminTestCipher{}
	var audited []string
	service := NewAdmin(store, cipher, func(_ context.Context, actor uint, path string, exact bool) error {
		if actor != 1 || path != c.AdminContentPath || !exact {
			t.Fatal("missing exact content authorization")
		}
		return nil
	}, func(_ context.Context, actor, session, message uint, outcome string) error {
		if actor != 1 || session != 2 || message != 3 {
			t.Fatal("wrong audit IDs")
		}
		audited = append(audited, outcome)
		return nil
	})
	service.now = func() time.Time { return now }
	content, err := service.Content(context.Background(), 1, 2, 3)
	if err != nil || !content.Available || content.Text != "hello" || content.Caption != "caption" {
		t.Fatal(content, err)
	}
	raw, _ := json.Marshal(content)
	for _, secret := range []string{"secret", "chat_id", "file_id", "payload", "claim"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leaked field", string(raw))
		}
	}
	for _, reason := range []string{"expired", "cleared"} {
		store.payload.RetainUntil = now.Add(time.Hour)
		if reason == "expired" {
			store.payload.RetainUntil = now
		} else {
			store.payload.Cipher = ""
		}
		content, err = service.Content(context.Background(), 1, 2, 3)
		if err != nil || content.Available || content.Reason != reason || content.Text != "" {
			t.Fatal(content, err)
		}
	}
	if cipher.calls != 1 || strings.Join(audited, ",") != "viewed,expired,cleared" {
		t.Fatal(cipher.calls, audited)
	}
	service.audit = func(context.Context, uint, uint, uint, string) error { return errors.New("audit failure") }
	if content, err = service.Content(context.Background(), 1, 2, 3); err == nil || content.Available {
		t.Fatal("audit failure leaked content")
	}
}

// TestAdminPaginationEmptyAndFailure covers bounded pagination, empty arrays and repository errors.
func TestAdminPaginationEmptyAndFailure(t *testing.T) {
	store := &adminTestStore{}
	service := NewAdmin(store, nil, func(context.Context, uint, string, bool) error { return nil }, nil)
	for _, query := range []c.AdminFilter{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 101}, {Page: 1000001, PageSize: 20}, {Page: 1, PageSize: 20, State: "arbitrary"}} {
		if _, err := service.Sessions(context.Background(), 1, query); !errors.Is(err, c.ErrAdminInvalid) {
			t.Fatal(err)
		}
	}
	page, err := service.Sessions(context.Background(), 1, c.AdminFilter{Page: 1, PageSize: 20})
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	store.err = c.ErrAdminNotFound
	if _, err = service.Messages(context.Background(), 1, 99, c.AdminFilter{Page: 1, PageSize: 20}); !errors.Is(err, c.ErrAdminNotFound) {
		t.Fatal(err)
	}
	store.err = errors.New("database failure")
	if _, err = service.Sessions(context.Background(), 1, c.AdminFilter{Page: 1, PageSize: 20}); err == nil {
		t.Fatal("failure swallowed")
	}
}
