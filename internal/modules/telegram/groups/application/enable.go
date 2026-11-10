package application

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// prepareTarget validates input and source CAS before any enable probe. It returns
// the canonical target, source sequence and a request-local creation probe. New
// targets ignore client names; edits retain the operator-controlled logical name.
func (s *Service) prepareTarget(ctx context.Context, id uint, in c.Input) (*d.Target, uint64, *c.ProbeResult, error) {
	if in.BotInstanceID == 0 || (in.Purpose != "support" && in.Purpose != "membership") {
		return nil, 0, nil, c.ErrInvalid
	}
	if id != 0 && (len(strings.TrimSpace(in.Name)) == 0 || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 120) {
		return nil, 0, nil, c.ErrInvalid
	}
	if _, err := s.Probe.Instance(ctx, in.BotInstanceID); err != nil {
		return nil, 0, nil, err
	}
	chat, resolved, err := s.resolveChatID(ctx, in.BotInstanceID, in.TelegramChatID)
	if err != nil {
		return nil, 0, nil, err
	}
	target := &d.Target{ID: id, Name: strings.TrimSpace(in.Name), Purpose: in.Purpose, BotInstanceID: in.BotInstanceID, TelegramChatID: chat, Enabled: in.Enabled, RouteVersion: 1, Observation: d.Observation{State: "unknown"}}
	if id == 0 {
		result, err := s.creationProbe(ctx, in.BotInstanceID, chat, resolved)
		if err != nil {
			return nil, 0, nil, err
		}
		target.Name = result.Observation.Title
		return target, 0, result, nil
	}
	old, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, 0, nil, err
	}
	changed := old.TelegramChatID != chat || old.BotInstanceID != in.BotInstanceID || old.Purpose != in.Purpose
	if old.RecoveryTaskID != 0 || old.Version != in.ExpectedVersion || old.RouteVersion != in.ExpectedRouteVersion || (old.Enabled && changed) {
		return nil, 0, nil, c.ErrConflict
	}
	target.RouteVersion = old.RouteVersion
	if changed {
		target.RouteVersion++
	}
	return target, old.ObservationSeq, nil, nil
}

// probeEnable performs a bounded Telegram check, or uses this request's creation
// result for the same target. It returns diagnostics through target; only complete,
// nonanonymous group capabilities enable it. Preview results are never reused by Save.
func (s *Service) probeEnable(ctx context.Context, target *d.Target, checked *c.ProbeResult) {
	if checked == nil {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		result := s.Probe.Check(probeCtx, target.BotInstanceID, target.TelegramChatID)
		checked = &result
	}
	result := *checked
	now := s.Now()
	observation := result.Observation
	observation.RouteVersion = target.RouteVersion
	observation.ErrorClass = result.ErrorClass
	observation.RetryAfterSeconds = result.RetryAfter
	observation.Source = "enable"
	observation.CheckedAt = &now
	observation.State = "unknown"
	if result.ErrorClass == "" {
		observation.ObservedAt = &now
		observation.LastSuccessAt = &now
		observation.State = "ready"
	} else {
		observation.LastFailureAt = &now
	}
	target.Observation = observation
	target.Enabled = result.ErrorClass == "" && observation.IsMember && len(Missing(*target)) == 0
	if result.ErrorClass == "" && !target.Enabled {
		target.Observation.State = "missing_permissions"
	}
}

// PreviewEnable checks a draft without registering, enabling or updating a group.
// id and in have the same write scope/CAS as Save; the returned view is not authorization.
func (s *Service) PreviewEnable(ctx context.Context, id uint, in c.Input) (*c.View, error) {
	target, _, checked, err := s.prepareTarget(ctx, id, in)
	if err != nil {
		return nil, err
	}
	s.probeEnable(ctx, target, checked)
	view := s.View(*target)
	return &view, nil
}

// saveTarget rechecks every requested enable; failed capability checks persist
// disabled with diagnostics. Creation requires a successful title lookup first.
// Existing CAS, recovery fences and observation races stay authoritative.
func (s *Service) saveTarget(ctx context.Context, id uint, in c.Input) (*c.Detail, error) {
	target, sequence, checked, err := s.prepareTarget(ctx, id, in)
	if err != nil {
		return nil, err
	}
	if in.Enabled {
		store, ok := s.Store.(c.CheckedStore)
		if !ok {
			return nil, c.ErrCapability
		}
		s.probeEnable(ctx, target, checked)
		err = store.SaveChecked(ctx, target, in.ExpectedVersion, in.ExpectedRouteVersion, sequence)
	} else {
		err = s.Store.Save(ctx, target, in.ExpectedVersion, in.ExpectedRouteVersion)
	}
	if err != nil {
		return nil, err
	}
	out, err := s.Detail(ctx, target.ID)
	if err == nil {
		out.EnableRejected = in.Enabled && !out.Enabled
	}
	return out, err
}
