package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
)

// WithDeliveryRecovery binds the transaction-aware recovery port and exact POST
// authorization. It returns s; configure once before exposing the admin service.
func (s *AdminService) WithDeliveryRecovery(store c.AdminDeliveryRecoverer, authorize c.AdminRecoveryAuthorize) *AdminService {
	s.deliveryRecovery, s.authorizeRecovery = store, authorize
	return s
}

// RecoverDelivery accepts only audit/CAS fields from HTTP. Direction/action select
// explicit routes, never SQL. It rechecks backend authorization after reading the
// current generation; all sends remain supervised by runtime's final guards.
func (s *AdminService) RecoverDelivery(ctx context.Context, actor, id uint, direction, action string, in c.AdminDeliveryRecoveryInput) (c.AdminDeliveryRecovery, error) {
	var out c.AdminDeliveryRecovery
	if actor == 0 {
		return out, c.ErrDenied
	}
	if id == 0 || (direction != "upstream" && direction != "downstream") || (action != "reconcile" && action != "retry") || in.ExpectedVersion == 0 || in.ExpectedVersion >= uint64(1<<63-1) || !adminEvidenceReference.MatchString(in.Evidence) {
		return out, c.ErrAdminInvalid
	}
	if s == nil || s.deliveryRecovery == nil || s.authorizeRecovery == nil || s.generation == nil {
		return out, c.ErrAdminUnavailable
	}
	path := c.AdminUpstreamPath + "/" + action
	if direction == "downstream" {
		path = c.AdminDownstreamPath + "/" + action
	}
	if err := s.authorizeRecovery(ctx, actor, path); err != nil {
		return out, err
	}
	var bot uint
	if direction == "upstream" {
		row, err := s.Upstream(ctx, actor, id)
		if err != nil {
			return out, err
		}
		bot = row.BotID
	} else {
		row, err := s.Downstream(ctx, actor, id)
		if err != nil {
			return out, err
		}
		bot = row.BotID
	}
	generation, err := s.generation(ctx, bot)
	if err != nil {
		return out, err
	}
	if generation == 0 {
		return out, c.ErrConflict
	}
	if err = s.authorizeRecovery(ctx, actor, path); err != nil {
		return out, err
	}
	out, err = s.deliveryRecovery.RecoverDelivery(ctx, bot, id, generation, actor, direction, action, in, s.now().UTC())
	logRecovery(actor, id, direction+"_"+action, err)
	return out, err
}
