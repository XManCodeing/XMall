package contract

// PageQuery selects a bounded numbered page; zero values default to page 1 and size 20.
// Cursor parameters must not be combined with this query.
type PageQuery struct {
	Page     int
	PageSize int
}

// Normalize validates requested page/size and offset overflow, returning defaults or ErrInvalid.
func (query PageQuery) Normalize() (PageQuery, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	if query.Page < 1 || query.PageSize < 1 || query.PageSize > 100 || query.Page-1 > int(^uint(0)>>1)/query.PageSize {
		return PageQuery{}, ErrInvalid
	}
	return query, nil
}

// PageInfo contains database-counted totals, never estimates from cursor history.
// Empty results have one navigable empty page; out-of-range requests clamp to the last page.
type PageInfo struct {
	Page      int   `json:"page"`
	PageSize  int   `json:"page_size"`
	Total     int64 `json:"total"`
	TotalPage int64 `json:"total_page"`
}
