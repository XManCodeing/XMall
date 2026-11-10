package application

import (
	"context"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	"log/slog"
)

// ListAccessTasks returns a bounded secret-free page; transport is responsible for admin RBAC.
func (service *AccessService) ListAccessTasks(ctx context.Context, bot, after uint, limit int) ([]c.AccessTask, error) {
	store, ok := service.Store.(c.AccessAdminRepository)
	if !ok {
		return nil, c.ErrCapability
	}
	rows, err := store.ListAccessTasks(ctx, bot, after, limit)
	if err != nil {
		return nil, err
	}
	return rows, service.enrichAccessDeliveries(ctx, bot, rows)
}

// enrichAccessDeliveries attaches existing runtime evidence only for the requested rows.
// bot scopes delivery reads; any failure is propagated instead of claiming missing delivery.
func (service *AccessService) enrichAccessDeliveries(ctx context.Context, bot uint, rows []c.AccessTask) error {
	if service.Deliveries != nil {
		for index := range rows {
			if rows[index].InviteID == 0 {
				continue
			}
			status, err := service.Deliveries.InviteDelivery(ctx, bot, rows[index].InviteID)
			if err != nil {
				return err
			}
			rows[index].Delivery = &status
		}
	}
	return nil
}

// AccessTasksPage forwards validated numbered pagination and reuses delivery enrichment.
// bot/page/size select one bounded page; total always comes from the read repository.
func (service *AccessService) AccessTasksPage(ctx context.Context, bot uint, page, size int, orderNo ...string) ([]c.AccessTask, int64, error) {
	if _, err := c.AccessOrderFilter(orderNo...); err != nil {
		return nil, 0, err
	}
	store, ok := service.Store.(c.AccessPageReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	rows, total, err := store.AccessTasksPage(ctx, bot, page, size, orderNo...)
	if err != nil {
		return nil, 0, err
	}
	if err := service.enrichAccessDeliveries(ctx, bot, rows); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AccessReviewsPage returns a bot-scoped repository page without scanning cursor history.
// page is one-based and size bounded by the repository; errors remain explicit.
func (service *AccessService) AccessReviewsPage(ctx context.Context, bot uint, page, size int) ([]c.AccessReview, int64, error) {
	store, ok := service.Store.(c.AccessPageReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	return store.AccessReviewsPage(ctx, bot, page, size)
}

// SearchAccessReviewsPage validates a bot-scoped keyword before delegating to the optional search port.
// Empty keywords preserve existing page readers; unavailable search returns an explicit capability error.
func (service *AccessService) SearchAccessReviewsPage(ctx context.Context, bot uint, page, size int, keyword string, status ...string) ([]c.AccessReview, int64, error) {
	_, filterErr := c.AccessReviewFilter(status...)
	keyword, _, err := c.LifecycleKeyword(keyword)
	if filterErr != nil || err != nil || bot == 0 || !c.ValidAdminPage(page, size) {
		return nil, 0, c.ErrInvalid
	}
	if keyword == "" && len(status) == 0 {
		return service.AccessReviewsPage(ctx, bot, page, size)
	}
	reader, ok := service.Store.(c.AccessReviewSearchReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	return reader.SearchAccessReviewsPage(ctx, bot, page, size, keyword, status...)
}

// LifecycleTasksPage uses the optional read port while preserving existing worker contracts.
// bot/page/size and optional status select one real page; unavailable adapters fail closed.
func (service *Lifecycle) LifecycleTasksPage(ctx context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleTask, int64, error) {
	if _, err := c.LifecycleFilterStates(status...); err != nil {
		return nil, 0, err
	}
	store, ok := service.Store.(c.LifecyclePageReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	return store.LifecycleTasksPage(ctx, bot, page, size, status...)
}

// LifecycleUsersPage validates bot/page/size/status and delegates to the optional grouped reader.
// It returns a user count with full task history, or a capability/validation/storage error.
func (service *Lifecycle) LifecycleUsersPage(ctx context.Context, bot uint, page, size int, status ...string) ([]c.LifecycleUserTasks, int64, error) {
	if _, err := c.LifecycleFilterStates(status...); err != nil || bot == 0 || !c.ValidAdminPage(page, size) {
		return nil, 0, c.ErrInvalid
	}
	store, ok := service.Store.(c.LifecycleUserPageReader)
	if !ok {
		return nil, 0, c.ErrCapability
	}
	return store.LifecycleUsersPage(ctx, bot, page, size, status...)
}

// SearchLifecycleUsersPage validates grouped search before invoking its optional repository capability.
// Empty keyword remains compatible with older readers; nonempty search never silently loses filtering.
func (service *Lifecycle) SearchLifecycleUsersPage(ctx context.Context, bot uint, page, size int, status, keyword string) ([]c.LifecycleUserTasks, int64, error) {
	if _, err := c.LifecycleFilterStates(status); err != nil || bot == 0 || !c.ValidAdminPage(page, size) {
		return nil, 0, c.ErrInvalid
	}
	keyword, _, err := c.LifecycleKeyword(keyword)
	if err != nil {
		return nil, 0, err
	}
	store, ok := service.Store.(c.LifecycleUserSearchReader)
	if !ok {
		if keyword == "" {
			return service.LifecycleUsersPage(ctx, bot, page, size, status)
		}
		return nil, 0, c.ErrCapability
	}
	return store.SearchLifecycleUsersPage(ctx, bot, page, size, status, keyword)
}

// ListAccessReviews returns unresolved inbox applications, including links without trusted provenance.
func (service *AccessService) ListAccessReviews(ctx context.Context, bot, after uint, limit int) ([]c.AccessReview, error) {
	store, ok := service.Store.(c.AccessAdminRepository)
	if !ok {
		return nil, c.ErrCapability
	}
	return store.ListAccessReviews(ctx, bot, after, limit)
}

// RecoverAccess queues a versioned recovery; no administrator HTTP request executes a platform side effect.
// The store atomically rechecks eligibility/binding and records accepted or blocked intent.
func (service *AccessService) RecoverAccess(ctx context.Context, bot, id, actor uint, input c.AccessRecoveryInput) error {
	store, ok := service.Store.(c.AccessAdminRepository)
	if !ok {
		return c.ErrCapability
	}
	if input.Action == "review_join" {
		return store.RecoverAccess(ctx, bot, id, actor, input, nil, service.accessNow())
	}
	task, err := store.AccessTask(ctx, bot, id)
	if err != nil {
		return err
	}
	var route *c.AccessRoute
	if input.Action == "retry" || input.Action == "rebuild" || input.Action == "refresh_member" {
		current, routeErr := service.Routes.MembershipRoute(ctx, task.TargetID)
		if routeErr == nil {
			route = &current
		}
	}
	err = store.RecoverAccess(ctx, bot, id, actor, input, route, service.accessNow())
	slog.InfoContext(ctx, "membership_admin_recovery", "operation_id", id, "actor_admin_id", actor, "accepted", err == nil)
	return err
}

var _ c.AccessAdmin = (*AccessService)(nil)
