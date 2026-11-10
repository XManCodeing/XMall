package telegramsupportrecovery

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	br "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	rc "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	tasks "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/infrastructure/gormstore"
	runtime "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	bots "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/gormstore"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/support/contract"
	"gorm.io/gorm"
)

// Permission checks the exact authenticated admin resume permission outside DB transactions.
type Permission func(context.Context, uint, string) error

type permitKey struct{}
type permit struct {
	Request   br.ModuleRequest
	Version   uint64
	Step      rd.Step
	GroupPlan *c.SupportRestoration
	Review    bool
}

// New constructs a production task-bound adapter. Missing permission or platform
// dependencies stay fail-closed; no membership receipt is produced by this adapter.
func New(db *gorm.DB, cipher c.Cipher, api runtime.ForumTopicAPI, permission Permission) *Adapter {
	a := &Adapter{DB: db, Cipher: cipher, API: api, Now: time.Now, Permission: permission}
	a.Authorize = taskAuthority
	return a
}

// taskAuthority checks the immutable per-target plan against the authenticated
// parent permit and live same-transaction scope/identity/capability evidence.
func taskAuthority(ctx context.Context, tx *gorm.DB, plan c.SupportRestoration, now time.Time) error {
	p, ok := ctx.Value(permitKey{}).(permit)
	if !ok || p.Request.TaskID != plan.TaskID || p.Request.ActorID != plan.ActorID {
		return c.ErrDenied
	}
	detail, err := checkParent(ctx, tx, p, now)
	if err != nil {
		return err
	}
	if p.GroupPlan != nil {
		expected := *p.GroupPlan
		if plan.Source.RouteVersion != expected.Source.RouteVersion || plan.Source.ChatID != expected.Source.ChatID {
			if err := checkHistoricalRoute(tx, expected, plan); err != nil {
				return err
			}
			plan.Source = expected.Source
		}
		if plan != expected {
			return c.ErrDenied
		}
		return checkGroupTarget(tx, detail, expected, now)
	}
	var route *rd.BotRoute
	for index := range detail.Snapshot.BotRoutes {
		candidate := &detail.Snapshot.BotRoutes[index]
		if candidate.TargetID == plan.Source.TargetID {
			route = candidate
			break
		}
	}
	if route == nil || route.Purpose != "support" {
		return c.ErrDenied
	}
	expected := supportPlan(p.Request, *route)
	if plan.Source.RouteVersion != expected.Source.RouteVersion || plan.Source.ChatID != expected.Source.ChatID {
		found := false
		for _, history := range detail.Snapshot.BotHistory {
			if history.TargetID == plan.Source.TargetID && history.RouteVersion == plan.Source.RouteVersion && history.ChatID == plan.Source.ChatID {
				found = true
			}
		}
		if !found {
			return c.ErrDenied
		}
		if err := checkHistoricalRoute(tx, expected, plan); err != nil {
			return err
		}
		plan.Source = expected.Source
	}
	if plan != expected {
		return c.ErrDenied
	}
	var target gd.Target
	if err = tx.First(&target, route.TargetID).Error; err != nil {
		return err
	}
	if target.RecoveryTaskID != plan.TaskID || target.BotInstanceID != plan.Destination.BotID || target.TelegramChatID != plan.Destination.ChatID || target.RouteVersion != plan.Destination.RouteVersion || target.Version != route.Version+2 || target.Purpose != "support" || target.Enabled != route.Enabled {
		return c.ErrDenied
	}
	observation := target.Observation
	if observation.Generation != plan.Destination.Generation || observation.BotTelegramID != detail.Snapshot.New.TelegramID || observation.RouteVersion != plan.Destination.RouteVersion || observation.State != "ready" || observation.ErrorClass != "" || observation.ObservedAt == nil || observation.ObservedAt.After(now) || now.Sub(*observation.ObservedAt) > time.Minute || len(gd.MissingCapabilities(target)) != 0 {
		return c.ErrDenied
	}
	return nil
}

// checkParent locks the existing parent and validates actor, full snapshot, all
// scopes, both runtime identities and the live execution step on one connection.
func checkParent(ctx context.Context, tx *gorm.DB, p permit, now time.Time) (*rc.Detail, error) {
	changed := tx.Model(&rd.Task{}).Where("id = ? AND version = ?", p.Request.TaskID, p.Version).UpdateColumn("version", gorm.Expr("version"))
	if changed.Error != nil {
		return nil, changed.Error
	}
	detail, err := tasks.New(tx).Get(ctx, p.Request.TaskID)
	if err != nil {
		return nil, err
	}
	if (detail.Task.Kind != rd.BotReplace && (detail.Task.Kind != rd.GroupReplace || p.GroupPlan == nil)) || (detail.Task.State != rd.Recovering && (!p.Review || (detail.Task.State != rd.NeedsReview && detail.Task.State != rd.PartialFailed))) || detail.Task.Version != p.Version || detail.Task.ConfirmedAt == nil || !reflect.DeepEqual(detail.Snapshot, p.Request.Snapshot) {
		return nil, c.ErrDenied
	}
	if err = checkActor(tx, p.Request.ActorID); err != nil {
		return nil, err
	}
	if err = checkScopes(tx, detail); err != nil {
		return nil, err
	}
	if detail.Task.Kind == rd.GroupReplace {
		if err = checkGroupRuntime(ctx, tx, detail, *p.GroupPlan); err != nil {
			return nil, err
		}
	} else {
		for index, endpoint := range []rd.Endpoint{detail.Snapshot.Old, detail.Snapshot.New} {
			versions := detail.Snapshot.ExpectedOld
			if index == 1 {
				versions = detail.Snapshot.ExpectedNew
			}
			instance, e := bots.New(tx).Get(ctx, endpoint.BotID)
			if e != nil {
				return nil, e
			}
			if instance == nil || instance.Mode != "internal" || instance.TelegramID != endpoint.TelegramID || instance.Version != versions.Config || instance.Generation != versions.Generation || instance.RecoveryTaskID != p.Request.TaskID || instance.RecoveryVersion != versions.Config || instance.RecoveryGeneration != versions.Generation || (index == 1 && instance.Enabled) {
				return nil, c.ErrDenied
			}
		}
		if err = checkInventory(tx, detail.Snapshot, p.Request.TaskID); err != nil {
			return nil, err
		}
	}
	if p.Step.ID != 0 {
		var count int64
		err = tx.Model(&rd.Step{}).Where("id = ? AND task_id = ? AND state = ? AND version = ? AND lease_token = ? AND lease_until > ?", p.Step.ID, p.Request.TaskID, rd.StepRunning, p.Step.Version, p.Step.LeaseToken, now).Count(&count).Error
		if err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, c.ErrDenied
		}
	}
	return detail, nil
}

// checkActor rechecks actual administrator existence and exact persisted grant;
// no global Casbin connection or wildcard authorization is used in a transaction.
func checkActor(tx *gorm.DB, actor uint) error {
	return checkActorPermission(tx, actor, "/admin/telegram-bot/recovery/tasks/:id/resume", "POST")
}

// checkActorPermission checks one exact persisted permission on the current transaction.
func checkActorPermission(tx *gorm.DB, actor uint, path, method string) error {
	var admin struct {
		ID      uint
		IsSuper bool
	}
	if actor == 0 {
		return c.ErrDenied
	}
	if err := tx.Table("admins").Where("id = ? AND deleted_at IS NULL", actor).Take(&admin).Error; err != nil {
		return c.ErrDenied
	}
	if admin.IsSuper {
		return nil
	}
	subject := fmt.Sprintf("admin:%d", actor)
	var count int64
	err := tx.Table("casbin_rule").Where("ptype = ? AND v1 = ? AND v2 = ?", "p", path, method).Where("v0 = ? OR v0 IN (SELECT v1 FROM casbin_rule WHERE ptype = ? AND v0 = ?)", subject, "g", subject).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return c.ErrDenied
	}
	return nil
}

// checkScopes requires equality, not mere presence of the requested target slot.
func checkScopes(tx *gorm.DB, detail *rc.Detail) error {
	var slots []rd.ScopeSlot
	if err := tx.Where("task_id = ?", detail.Task.ID).Find(&slots).Error; err != nil {
		return err
	}
	actual, expected := []string{}, []string{}
	for _, slot := range slots {
		actual = append(actual, slot.Key)
	}
	scope := detail.Snapshot.Scope
	for prefix, values := range map[string][]string{"bot": scope.BotIDs, "target": scope.TargetIDs, "chat": scope.ChatIDs, "telegram": scope.TelegramIDs, "account": scope.AccountIDs} {
		for _, value := range values {
			expected = append(expected, prefix+":"+value)
		}
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		return c.ErrDenied
	}
	return nil
}

// checkInventory rejects switched-target/module drift and verifies every saved
// historical route still belongs to the same old bot and physical chat.
func checkInventory(tx *gorm.DB, snapshot rd.Snapshot, task uint) error {
	var targets []gd.Target
	if err := tx.Where("bot_instance_id IN ?", []uint{snapshot.Old.BotID, snapshot.New.BotID}).Find(&targets).Error; err != nil {
		return err
	}
	if len(targets) != len(snapshot.BotRoutes) {
		return c.ErrDenied
	}
	for _, expected := range snapshot.BotRoutes {
		found := false
		for _, target := range targets {
			if target.ID == expected.TargetID {
				found = target.BotInstanceID == snapshot.New.BotID && target.TelegramChatID == expected.DestinationChat() && target.Version == expected.Version+2 && target.RouteVersion == expected.RouteVersion+1 && target.Purpose == expected.Purpose && target.Enabled == expected.Enabled && target.RecoveryTaskID == task
			}
		}
		if !found {
			return c.ErrDenied
		}
	}
	for _, history := range snapshot.BotHistory {
		var count int64
		if err := tx.Model(&gd.Route{}).Where("target_id = ? AND route_version = ? AND bot_instance_id = ? AND telegram_chat_id = ?", history.TargetID, history.RouteVersion, snapshot.Old.BotID, history.ChatID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return c.ErrDenied
		}
	}
	var modules []gd.ModuleConfig
	if err := tx.Where("bot_instance_id IN ?", []uint{snapshot.Old.BotID, snapshot.New.BotID}).Find(&modules).Error; err != nil {
		return err
	}
	if len(modules) != 2*len(snapshot.BotModules) {
		return c.ErrDenied
	}
	for _, expected := range snapshot.BotModules {
		source, destination := false, false
		for _, module := range modules {
			if module.Partition != expected.Partition {
				continue
			}
			if module.BotInstanceID == snapshot.Old.BotID {
				source = module.Version == expected.Version+1 && !module.Enabled && module.TargetID == 0
			}
			if module.BotInstanceID == snapshot.New.BotID {
				destination = module.Version == 1 && module.Enabled == expected.Enabled && module.TargetID == expected.TargetID
			}
		}
		if !source || !destination {
			return c.ErrDenied
		}
	}
	return nil
}

// checkHistoricalRoute grants only older routes of this exact logical target
// and original bot. Another target/chat cannot be smuggled into restoration.
func checkHistoricalRoute(tx *gorm.DB, current, candidate c.SupportRestoration) error {
	source := candidate.Source
	if source.BotID != current.Source.BotID || source.TargetID != current.Source.TargetID || source.Version != current.Source.Version || source.Generation != current.Source.Generation || source.RouteVersion == 0 || source.RouteVersion >= current.Source.RouteVersion {
		return c.ErrDenied
	}
	var count int64
	if err := tx.Model(&gd.Route{}).Where("target_id = ? AND route_version = ? AND bot_instance_id = ? AND telegram_chat_id = ?", source.TargetID, source.RouteVersion, source.BotID, source.ChatID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return c.ErrDenied
	}
	return nil
}

// checkGroupRuntime verifies the group snapshot's generation (its Config is a
// group version, not a runtime version) against a real task-owned runtime fence.
func checkGroupRuntime(ctx context.Context, tx *gorm.DB, detail *rc.Detail, plan c.SupportRestoration) error {
	snapshot := detail.Snapshot
	if snapshot.Old.BotID == 0 || snapshot.Old.BotID != snapshot.New.BotID || snapshot.Old.TargetID == 0 || snapshot.Old.TargetID != snapshot.New.TargetID || snapshot.Old.ChatID >= 0 || snapshot.New.ChatID >= 0 || snapshot.Old.ChatID == snapshot.New.ChatID || snapshot.ExpectedOld.Config == 0 || snapshot.ExpectedOld.Route == 0 || snapshot.ExpectedNew.Generation == 0 {
		return c.ErrDenied
	}
	instance, err := bots.New(tx).Get(ctx, snapshot.New.BotID)
	if err != nil {
		return err
	}
	if instance.Mode != "internal" || instance.TelegramID <= 0 || instance.Generation != snapshot.ExpectedNew.Generation || instance.RecoveryTaskID != detail.Task.ID || instance.RecoveryVersion != instance.Version || instance.RecoveryGeneration != instance.Generation {
		return c.ErrDenied
	}
	expected := groupPlan(detail, plan.ActorID, instance.Version)
	if plan != expected {
		return c.ErrDenied
	}
	return bots.New(tx).RecoveryDrained(ctx, runtime.RecoveryFence{TaskID: detail.Task.ID, BotID: instance.ID, Version: instance.Version, Generation: instance.Generation})
}

// groupPlan maps immutable group CAS and runtime fence into the existing support core.
func groupPlan(detail *rc.Detail, actor uint, version uint64) c.SupportRestoration {
	snapshot := detail.Snapshot
	return c.SupportRestoration{TaskID: detail.Task.ID, ActorID: actor, Evidence: fmt.Sprintf("audit:recovery-%d", detail.Task.ID),
		Source:      c.RestorationRoute{BotID: snapshot.Old.BotID, TargetID: snapshot.Old.TargetID, Version: version, Generation: snapshot.ExpectedNew.Generation, RouteVersion: snapshot.ExpectedOld.Route, ChatID: snapshot.Old.ChatID},
		Destination: c.RestorationRoute{BotID: snapshot.New.BotID, TargetID: snapshot.New.TargetID, Version: version, Generation: snapshot.ExpectedNew.Generation, RouteVersion: snapshot.ExpectedOld.Route + 1, ChatID: snapshot.New.ChatID}}
}

// checkGroupTarget requires the real switched target, saved old route and fresh
// forum observation bound to the current bot identity, generation and sequence.
func checkGroupTarget(tx *gorm.DB, detail *rc.Detail, plan c.SupportRestoration, now time.Time) error {
	var target gd.Target
	if err := tx.First(&target, plan.Destination.TargetID).Error; err != nil {
		return err
	}
	if target.RecoveryTaskID != plan.TaskID || target.BotInstanceID != plan.Destination.BotID || target.TelegramChatID != plan.Destination.ChatID || target.RouteVersion != plan.Destination.RouteVersion || target.Version != detail.Snapshot.ExpectedOld.Config+2 || target.Purpose != "support" {
		return c.ErrDenied
	}
	instance, err := bots.New(tx).Get(tx.Statement.Context, plan.Destination.BotID)
	if err != nil {
		return err
	}
	observation := target.Observation
	if observation.BotTelegramID != instance.TelegramID || observation.Generation != plan.Destination.Generation || observation.RouteVersion != plan.Destination.RouteVersion || observation.Sequence != target.ObservationSeq || observation.State != "ready" || observation.ErrorClass != "" || observation.ObservedAt == nil || observation.ObservedAt.After(now) || now.Sub(*observation.ObservedAt) > time.Minute || len(gd.MissingCapabilities(target)) != 0 {
		return c.ErrDenied
	}
	var count int64
	if err = tx.Model(&gd.Route{}).Where("target_id = ? AND route_version = ? AND bot_instance_id = ? AND telegram_chat_id = ?", plan.Source.TargetID, plan.Source.RouteVersion, plan.Source.BotID, plan.Source.ChatID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return c.ErrDenied
	}
	return nil
}

// supportPlan derives both route sides solely from the persisted parent snapshot.
func supportPlan(request br.ModuleRequest, route rd.BotRoute) c.SupportRestoration {
	snapshot := request.Snapshot
	return c.SupportRestoration{TaskID: request.TaskID, ActorID: request.ActorID, Evidence: fmt.Sprintf("audit:recovery-%d", request.TaskID),
		Source:      c.RestorationRoute{BotID: snapshot.Old.BotID, TargetID: route.TargetID, Version: snapshot.ExpectedOld.Config, Generation: snapshot.ExpectedOld.Generation, RouteVersion: route.RouteVersion, ChatID: route.ChatID},
		Destination: c.RestorationRoute{BotID: snapshot.New.BotID, TargetID: route.TargetID, Version: snapshot.ExpectedNew.Config, Generation: snapshot.ExpectedNew.Generation, RouteVersion: route.RouteVersion + 1, ChatID: route.ChatID}}
}
