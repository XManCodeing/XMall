package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"time"
)

type Service struct {
	Store c.Store
	Probe c.Probe
	Now   func() time.Time
}

func New(store c.Store, probe c.Probe) *Service {
	return &Service{Store: store, Probe: probe, Now: func() time.Time { return time.Now().UTC() }}
}

const Freshness = 5 * time.Minute

// Missing returns absent purpose-specific capabilities for the target snapshot.
func Missing(t d.Target) []string { return d.MissingCapabilities(t) }
func (s *Service) View(t d.Target) c.View {
	v := c.View{Target: t, MissingCapabilities: Missing(t), AffectedFunctions: []string{}, Checking: t.Observation.State == "checking"}
	if t.Observation.ObservedAt != nil {
		until := t.Observation.ObservedAt.Add(Freshness)
		v.FreshUntil = &until
		v.Fresh = t.RecoveryTaskID == 0 && t.Observation.RouteVersion == t.RouteVersion && s.Now().Before(until) && t.Observation.ErrorClass == "" && !v.Checking
	}
	if !v.Fresh || t.Observation.State != "ready" {
		v.AffectedFunctions = append(v.AffectedFunctions, t.Purpose)
	}
	return v
}
func (s *Service) List(ctx context.Context, f c.Filter) ([]c.View, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	rows, total, err := s.Store.List(ctx, f)
	out := make([]c.View, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.View(row))
	}
	return out, total, err
}
func (s *Service) Detail(ctx context.Context, id uint) (*c.Detail, error) {
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	routes, ops, err := s.Store.History(ctx, id)
	if err != nil {
		return nil, err
	}
	return &c.Detail{View: s.View(*t), Routes: routes, Operations: ops}, nil
}

// Save persists input with a fresh server-side check for every requested enable.
func (s *Service) Save(ctx context.Context, id uint, in c.Input) (*c.Detail, error) {
	return s.saveTarget(ctx, id, in)
}
func (s *Service) Check(ctx context.Context, id uint, route uint64, actor uint, source string) (*c.Detail, error) {
	now := s.Now()
	target, op, err := s.Store.BeginCheck(ctx, id, route, source, actor, now)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := s.Probe.Check(probeCtx, target.BotInstanceID, target.TelegramChatID)
	o := result.Observation
	o.RouteVersion = route
	o.Sequence = op.Sequence
	o.Source = source
	o.CheckedAt = &now
	o.ErrorClass = result.ErrorClass
	o.RetryAfterSeconds = result.RetryAfter
	finished := s.Now()
	next := finished.Add(time.Minute)
	if result.ErrorClass != "" {
		o.State = "unknown"
		o.LastFailureAt = &finished
		switch result.ErrorClass {
		case "resource_inaccessible", "permission_denied":
			o.State = "inaccessible"
		case "anonymous_admin":
			o.State = "missing_permissions"
		case "authentication":
			o.State = "authentication_error"
		case "rate_limited":
			o.State = "rate_limited"
		}
		if result.RetryAfter > 0 {
			next = finished.Add(time.Duration(result.RetryAfter) * time.Second)
		}
	} else {
		o.ObservedAt = &finished
		o.LastSuccessAt = &finished
		target.Observation = o
		o.State = "ready"
		if o.MemberStatus == "left" || o.MemberStatus == "kicked" {
			o.State = "left"
		} else if len(Missing(*target)) > 0 {
			o.State = "missing_permissions"
		}
		next = finished.Add(2 * time.Minute)
	}
	// Cancellation leaves a durable checking operation; bounded recovery supersedes it.
	if _, err = s.Store.FinishCheck(ctx, op, o, next); err != nil {
		return nil, err
	}
	return s.Detail(ctx, id)
}
func (s *Service) Reconcile(ctx context.Context, bot uint) error {
	identity, err := s.Probe.Instance(ctx, bot)
	if err != nil {
		return err
	}
	if !identity.Running {
		return nil
	}
	rows, err := s.Store.Due(ctx, bot, identity.Generation, s.Now(), 2)
	if err != nil {
		return err
	}
	for _, t := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = s.Check(ctx, t.ID, t.RouteVersion, 0, "periodic")
	}
	return nil
}

// Events are independent of operator identity and never auto-bind a group. They
// invalidate in-flight observations before a fresh read, including lost-event recovery.
func (s *Service) Handle(ctx context.Context, bot uint, u r.Update) ([]r.Message, error) {
	if u.Type != "my_chat_member" || u.Member == nil || u.ChatID >= 0 {
		return nil, nil
	}
	identity, err := s.Probe.Instance(ctx, bot)
	if err != nil {
		return nil, err
	}
	if u.Member.Subject.ID != identity.TelegramID {
		return nil, nil
	}
	return nil, s.Store.Invalidate(ctx, bot, u.ChatID, s.Now())
}
func (s *Service) AllowedUpdates(context.Context, uint) ([]string, error) {
	return []string{"my_chat_member"}, nil
}
func (s *Service) Modules(ctx context.Context, bot uint) ([]c.ModuleView, error) {
	if _, err := s.Probe.Instance(ctx, bot); err != nil {
		return nil, err
	}
	rows, err := s.Store.Modules(ctx, bot)
	if err != nil {
		return nil, err
	}
	for _, partition := range []string{"support", "membership"} {
		found := false
		for _, row := range rows {
			if row.Partition == partition {
				found = true
			}
		}
		if !found {
			rows = append(rows, c.ModuleView{ModuleConfig: d.ModuleConfig{BotInstanceID: bot, Partition: partition}})
		}
	}
	return rows, nil
}
func (s *Service) SaveModule(ctx context.Context, bot, actor uint, in c.ModuleInput) ([]c.ModuleView, error) {
	if in.Partition != "support" && in.Partition != "membership" {
		return nil, c.ErrInvalid
	}
	if _, err := s.Probe.Instance(ctx, bot); err != nil {
		return nil, err
	}
	if in.TargetID != 0 {
		target, err := s.Store.Get(ctx, in.TargetID)
		if err != nil {
			return nil, err
		}
		if target.BotInstanceID != bot || target.Purpose != in.Partition {
			return nil, c.ErrInvalid
		}
	}
	v := d.ModuleConfig{BotInstanceID: bot, Partition: in.Partition, Enabled: in.Enabled, TargetID: in.TargetID, UpdatedBy: actor}
	if err := s.Store.SaveModule(ctx, &v, in.ExpectedVersion); err != nil {
		return nil, err
	}
	return s.Modules(ctx, bot)
}
