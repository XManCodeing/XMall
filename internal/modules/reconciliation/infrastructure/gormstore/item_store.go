package gormstore

import (
	reconciliationcontract "github.com/XManCodeing/XMall/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/XManCodeing/XMall/internal/modules/reconciliation/domain"

	"gorm.io/gorm"
)

type ItemStore struct {
	db *gorm.DB
}

var _ reconciliationcontract.ItemRepository = (*ItemStore)(nil)

func NewItemStore(db *gorm.DB) *ItemStore { return &ItemStore{db: db} }

func (s *ItemStore) BatchCreate(items []reconciliationdomain.Item) error {
	if len(items) == 0 {
		return nil
	}
	return s.db.Create(&items).Error
}

func (s *ItemStore) GetByID(id uint) (*reconciliationdomain.Item, error) {
	var item reconciliationdomain.Item
	if err := s.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *ItemStore) Update(item *reconciliationdomain.Item) error {
	result := s.db.Model(&reconciliationdomain.Item{}).Where("id = ?", item.ID).
		Updates(map[string]any{"resolved": item.Resolved, "resolved_by": item.ResolvedBy,
			"resolved_at": item.ResolvedAt, "remark": item.Remark})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return reconciliationcontract.ErrItemNotFound
	}
	return nil
}

func (s *ItemStore) ListByJobID(jobID uint, page, pageSize int) ([]reconciliationdomain.Item, int64, error) {
	var items []reconciliationdomain.Item
	var total int64
	query := s.db.Model(&reconciliationdomain.Item{}).Where("job_id = ?", jobID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
