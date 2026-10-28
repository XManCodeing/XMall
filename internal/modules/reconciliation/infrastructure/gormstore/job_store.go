package gormstore

import (
	"time"

	"github.com/homles698698-lab/XMall/internal/constants"
	reconciliationcontract "github.com/homles698698-lab/XMall/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/homles698698-lab/XMall/internal/modules/reconciliation/domain"

	"gorm.io/gorm"
)

type JobStore struct {
	db *gorm.DB
}

var _ reconciliationcontract.JobRepository = (*JobStore)(nil)

func NewJobStore(db *gorm.DB) *JobStore { return &JobStore{db: db} }

func (s *JobStore) Claim(id uint, startedAt time.Time) (bool, error) {
	result := s.db.Model(&reconciliationdomain.Job{}).
		Where("id = ? AND status IN ?", id, []string{constants.ReconciliationJobStatusPending, constants.ReconciliationJobStatusFailed}).
		Updates(map[string]any{"status": constants.ReconciliationJobStatusRunning, "started_at": startedAt, "finished_at": nil})
	return result.RowsAffected == 1, result.Error
}

// Complete replaces only unresolved differences. Resolved rows remain immutable
// audit evidence, even when the order no longer differs on this attempt.
func (s *JobStore) Complete(job *reconciliationdomain.Job, items []reconciliationdomain.Item) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := ownedJob(tx, job).Updates(map[string]any{
			"status": constants.ReconciliationJobStatusCompleted, "finished_at": job.FinishedAt,
			"total_count": job.TotalCount, "matched_count": job.MatchedCount,
			"mismatched_count": job.MismatchedCount, "result_json": job.ResultJSON,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return reconciliationcontract.ErrJobRunning
		}
		if err := tx.Where("job_id = ? AND resolved = ?", job.ID, false).Delete(&reconciliationdomain.Item{}).Error; err != nil {
			return err
		}
		var resolved []reconciliationdomain.Item
		if err := tx.Where("job_id = ? AND resolved = ?", job.ID, true).Find(&resolved).Error; err != nil {
			return err
		}
		seen := make(map[uint]bool, len(resolved))
		for _, item := range resolved {
			seen[item.ProcurementOrderID] = true
		}
		pending := make([]reconciliationdomain.Item, 0, len(items))
		for _, item := range items {
			if seen[item.ProcurementOrderID] {
				continue
			}
			seen[item.ProcurementOrderID] = true
			item.ID, item.JobID = 0, job.ID
			pending = append(pending, item)
		}
		if len(pending) == 0 {
			return nil
		}
		return tx.Create(&pending).Error
	})
}

func (s *JobStore) Fail(job *reconciliationdomain.Job) error {
	result := ownedJob(s.db, job).Updates(map[string]any{
		"status": constants.ReconciliationJobStatusFailed, "finished_at": job.FinishedAt,
		"result_json": job.ResultJSON,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return reconciliationcontract.ErrJobRunning
	}
	return nil
}

func ownedJob(db *gorm.DB, job *reconciliationdomain.Job) *gorm.DB {
	return db.Model(&reconciliationdomain.Job{}).
		Where("id = ? AND status = ? AND started_at = ?", job.ID, constants.ReconciliationJobStatusRunning, job.StartedAt)
}

func (s *JobStore) Create(job *reconciliationdomain.Job) error {
	return s.db.Create(job).Error
}

func (s *JobStore) GetByID(id uint) (*reconciliationdomain.Job, error) {
	var job reconciliationdomain.Job
	if err := s.db.Preload("Connection", "deleted_at IS NULL").First(&job, id).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *JobStore) Update(job *reconciliationdomain.Job) error {
	return s.db.Save(job).Error
}

func (s *JobStore) List(filter reconciliationcontract.JobListFilter) ([]reconciliationdomain.Job, int64, error) {
	var jobs []reconciliationdomain.Job
	var total int64
	query := s.db.Model(&reconciliationdomain.Job{})
	if filter.ConnectionID > 0 {
		query = query.Where("connection_id = ?", filter.ConnectionID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if err := query.Preload("Connection", "deleted_at IS NULL").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}
	return jobs, total, nil
}
