package application

import (
	"context"
	"errors"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// resumeLocalGroup uses the optional narrow store port before normal execution.
// Only one exact local intent is eligible; database time decides lease validity.
// Current RBAC is checked again immediately before the atomic parent/step CAS.
func (executor *GroupExecutor) resumeLocalGroup(ctx context.Context, detail *c.Detail, actor uint) (*c.Detail, error) {
	port, ok := executor.Store.(c.LocalGroupResumer)
	if !ok {
		return detail, nil
	}
	page, err := executor.Store.ListSteps(ctx, detail.Task.ID, 0, 100)
	if err != nil {
		return nil, err
	}
	if page.NextAfterID != 0 {
		return detail, nil
	}
	for _, step := range page.Items {
		if step.BusinessKey == "group.recover_module" && step.Operation == "recover_module" {
			resumer, available := executor.Modules.(c.GroupModuleResumer)
			if !available || (step.State != d.StepFailed && step.State != d.StepUnknown && step.State != d.StepRunning) {
				continue
			}
			if err := executor.Authorizer.AuthorizeGroupRecovery(ctx, actor, detail.Snapshot.New.TargetID); err != nil {
				return nil, err
			}
			if err := executor.prepareModuleResume(ctx, detail, actor); err != nil {
				return nil, err
			}
			task, err := resumer.ResumeGroupModule(ctx, c.LocalGroupRetry{TaskID: detail.Task.ID, TaskVersion: detail.Task.Version,
				StepID: step.ID, StepVersion: step.Version, ActorID: actor})
			if err != nil {
				return nil, err
			}
			detail.Task = *task
			return detail, nil
		}
		if step.BusinessKey != "group."+step.Operation || (step.Operation != "freeze" && step.Operation != "switch_route" && step.Operation != "check_route" && step.Operation != "release") {
			continue
		}
		if step.State != d.StepFailed && step.State != d.StepUnknown && step.State != d.StepRunning {
			continue
		}
		if err := executor.Authorizer.AuthorizeGroupRecovery(ctx, actor, detail.Snapshot.Old.TargetID); err != nil {
			return nil, err
		}
		task, err := port.RetryLocalGroupStep(ctx, c.LocalGroupRetry{TaskID: detail.Task.ID, TaskVersion: detail.Task.Version,
			StepID: step.ID, StepVersion: step.Version, ActorID: actor})
		if err != nil {
			return nil, err
		}
		detail.Task = *task
		return detail, nil
	}
	return detail, nil
}

// prepareModuleResume refreshes only the read-only capability probe outside SQL.
// detail binds the route/generation; a live wrapper lease is rejected by the CAS.
func (executor *GroupExecutor) prepareModuleResume(ctx context.Context, detail *c.Detail, actor uint) error {
	_, err := executor.checkedGroup(ctx, detail.Task.ID, detail.Snapshot)
	if err == nil || !errors.Is(err, groups.ErrCapability) {
		return err
	}
	if _, err := executor.Groups.Check(ctx, detail.Snapshot.New.TargetID, detail.Snapshot.ExpectedOld.Route+1, actor, "recovery"); err != nil {
		return err
	}
	_, err = executor.checkedGroup(ctx, detail.Task.ID, detail.Snapshot)
	return err
}
