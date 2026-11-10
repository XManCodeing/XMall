package gormstore

import (
	"context"
	"encoding/json"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
)

// Get 按 id 返回父任务和 typed 无秘密预检快照；缺失返回 ErrNotFound。
func (s *Store) Get(ctx context.Context, id uint) (*c.Detail, error) {
	if id == 0 {
		return nil, c.ErrInvalid
	}
	var detail c.Detail
	if err := s.db.WithContext(ctx).First(&detail.Task, id).Error; err != nil {
		return nil, mapped(err)
	}
	if err := json.Unmarshal([]byte(detail.Task.SnapshotJSON), &detail.Snapshot); err != nil {
		return nil, err
	}
	return &detail, nil
}

// pageLimit 校验 limit，返回默认或显式的页大小及错误。
func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return 20, nil
	}
	if limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	return limit, nil
}

// List 按 filter 返回有界 ID 游标分页，不查询秘密字段或执行平台调用。
func (s *Store) List(ctx context.Context, filter c.ListFilter) (c.TaskPage, error) {
	page := c.TaskPage{Items: []d.Task{}}
	limit, err := pageLimit(filter.Limit)
	if err != nil {
		return page, err
	}
	if filter.Kind != "" && !validKind(filter.Kind) || filter.State != "" && !validState(filter.State) {
		return page, c.ErrInvalid
	}
	query := s.db.WithContext(ctx).Where("id > ?", filter.AfterID)
	if filter.Kind != "" {
		query = query.Where("kind = ?", filter.Kind)
	}
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	if err := query.Order("id ASC").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfterID = page.Items[limit-1].ID
	}
	return page, nil
}

// ListSteps 按 taskID/afterID/limit 返回有界步骤页，任务缺失返回 ErrNotFound。
func (s *Store) ListSteps(ctx context.Context, taskID, afterID uint, limit int) (c.StepPage, error) {
	page := c.StepPage{Items: []d.Step{}}
	if taskID == 0 {
		return page, c.ErrInvalid
	}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	var task d.Task
	if err := s.db.WithContext(ctx).Select("id").First(&task, taskID).Error; err != nil {
		return page, mapped(err)
	}
	if err := s.db.WithContext(ctx).Where("task_id = ? AND id > ?", taskID, afterID).Order("id ASC").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfterID = page.Items[limit-1].ID
	}
	return page, nil
}
