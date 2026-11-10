package contract

import "context"

// SupportEntryPage is a bounded, secret-free task projection; IDs remain decimal strings.
type SupportEntryPage struct {
	TaskVersion uint64         `json:"task_version,string"`
	Items       []SupportEntry `json:"items"`
	NextAfterID uint           `json:"next_after_id,string"`
}

// SupportEntry exposes checkpoint CAS and liabilities, never customer payloads or claims.
type SupportEntry struct {
	ID          uint   `json:"id,string"`
	TargetID    uint   `json:"target_id,string"`
	Revision    uint64 `json:"revision,string"`
	State       string `json:"state"`
	Legacy      uint64 `json:"legacy,string"`
	OperationID uint   `json:"operation_id,string"`
}

// SupportOperationPage enumerates current and retired operations without private content.
type SupportOperationPage struct {
	TaskVersion uint64             `json:"task_version,string"`
	Items       []SupportOperation `json:"items"`
	NextAfterID uint               `json:"next_after_id,string"`
}

// SupportOperation carries exact CAS for a narrowly scoped topic decision.
type SupportOperation struct {
	ID      uint   `json:"id,string"`
	Version uint64 `json:"version,string"`
	State   string `json:"state"`
	Retired bool   `json:"retired"`
	// Route fields are read-only evidence; resolve accepts none of them.
	TargetID     uint   `json:"target_id,string"`
	RouteVersion uint64 `json:"route_version,string"`
	ChatID       int64  `json:"chat_id,string"`
	Action       string `json:"action"`
}

// SupportResolution accepts intent and CAS only; actor/task/route are server-owned.
type SupportResolution struct {
	TaskVersion      uint64 `json:"task_version,string"`
	Revision         uint64 `json:"revision,string"`
	OperationID      uint   `json:"operation_id,string"`
	OperationVersion uint64 `json:"operation_version,string"`
	Decision         string `json:"decision"`
	Evidence         string `json:"evidence"`
	ThreadID         int64  `json:"thread_id,string"`
}

// SupportManagement is an authenticated management port. Mutations never send
// HTTP or force parent completion; implementations recheck exact grants in DB.
type SupportManagement interface {
	SupportEntries(context.Context, uint, uint, uint, int) (SupportEntryPage, error)
	SupportOperations(context.Context, uint, uint, uint, uint, int) (SupportOperationPage, error)
	ResolveSupport(context.Context, uint, uint, uint, SupportResolution) error
}
