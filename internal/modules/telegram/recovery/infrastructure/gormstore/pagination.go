package gormstore

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
)

// numberedPage counts the exact filtered query and clamps its page before bounded SQL retrieval.
// query must already carry the transaction, model and all authorization/filter scopes.
func numberedPage(query *gorm.DB, input c.PageQuery) (*c.PageInfo, error) {
	info := &c.PageInfo{Page: input.Page, PageSize: input.PageSize}
	if err := query.Count(&info.Total).Error; err != nil {
		return nil, err
	}
	info.TotalPage = (info.Total + int64(input.PageSize) - 1) / int64(input.PageSize)
	if info.TotalPage < 1 {
		info.TotalPage = 1
	}
	if int64(info.Page) > info.TotalPage {
		info.Page = int(info.TotalPage)
	}
	return info, nil
}

// ListPage returns a database-counted task page using the same kind/state scope for count and rows.
// Cursor inputs are rejected rather than silently reinterpreted; errors return no partial result.
func (s *Store) ListPage(ctx context.Context, filter c.ListFilter, input c.PageQuery) (c.TaskPage, error) {
	page := c.TaskPage{Items: []d.Task{}}
	input, err := input.Normalize()
	if err != nil || filter.AfterID != 0 || filter.Limit != 0 || filter.Kind != "" && !validKind(filter.Kind) || filter.State != "" && !validState(filter.State) {
		return page, c.ErrInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&d.Task{})
		if filter.Kind != "" {
			query = query.Where("kind = ?", filter.Kind)
		}
		if filter.State != "" {
			query = query.Where("state = ?", filter.State)
		}
		info, countErr := numberedPage(query, input)
		if countErr != nil {
			return countErr
		}
		page.PageInfo = info
		return query.Order("id ASC").Offset((info.Page - 1) * info.PageSize).Limit(info.PageSize).Find(&page.Items).Error
	})
	return page, err
}

// ListStepsPage returns database-counted checkpoints scoped to taskID; absent tasks return ErrNotFound.
// All count/read operations use the transaction handle, including SQLite's single connection.
func (s *Store) ListStepsPage(ctx context.Context, taskID uint, input c.PageQuery) (c.StepPage, error) {
	page := c.StepPage{Items: []d.Step{}}
	input, err := input.Normalize()
	if err != nil || taskID == 0 {
		return page, c.ErrInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task d.Task
		if err := tx.Select("id").First(&task, taskID).Error; err != nil {
			return mapped(err)
		}
		query := tx.Model(&d.Step{}).Where("task_id = ?", taskID)
		info, countErr := numberedPage(query, input)
		if countErr != nil {
			return countErr
		}
		page.PageInfo = info
		return query.Order("id ASC").Offset((info.Page - 1) * info.PageSize).Limit(info.PageSize).Find(&page.Items).Error
	})
	return page, err
}
