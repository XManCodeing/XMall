package contract

// AdminTopicDeletionPath grants only recording a verified physical deletion.
const AdminTopicDeletionPath = AdminTopicPath + "/confirm-deleted"

// AdminTopicMapping is a server-owned physical mapping snapshot. Decimal strings
// preserve all identifiers and CAS revisions through JavaScript JSON parsing.
type AdminTopicMapping struct {
	ID           uint   `json:"id,string"`
	Version      uint64 `json:"version,string"`
	ChatID       int64  `json:"chat_id,string"`
	ThreadID     int64  `json:"thread_id,string"`
	RouteVersion uint64 `json:"route_version,string"`
	State        string `json:"state"`
}

// AdminTopicDeletionInput confirms facts, not a request to delete on Telegram.
// All snapshot identifiers are compared to trusted storage before mutation.
type AdminTopicDeletionInput struct {
	ExpectedVersion uint64 `json:"expected_version,string"`
	MappingID       uint   `json:"mapping_id,string"`
	MappingVersion  uint64 `json:"mapping_version,string"`
	ChatID          int64  `json:"chat_id,string"`
	ThreadID        int64  `json:"thread_id,string"`
	Evidence        string `json:"evidence"`
	Confirmed       bool   `json:"confirmed"`
}
