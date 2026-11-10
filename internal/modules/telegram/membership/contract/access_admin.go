package contract

import (
	"context"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AccessTask is the administrative secret-free operation projection, including original provenance.
type AccessTask struct {
	Delivery *rt.InviteDeliveryStatus `json:"delivery,omitempty" gorm:"-"`
	AccessReconciliation
	// BuyerTelegramID is the purchaser's verified Telegram binding captured at checkout;
	// empty means no complete immutable purchase chain, never the current task applicant.
	BuyerTelegramID string     `json:"buyer_telegram_id"`
	OrderNo         string     `json:"order_no"`
	ChatTitle       string     `json:"chat_title"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	Kind            string     `json:"kind"`
	InviteID        uint       `json:"invite_id,string"`
	JoinID          uint       `json:"join_id,string"`
	AccountID       uint       `json:"account_id,string"`
	SourceID        uint       `json:"source_id,string"`
	Version         uint64     `json:"version,string"`
	BindingVersion  uint       `json:"binding_version,string"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Actions         []string   `json:"actions" gorm:"-"`
	BlockedReason   string     `json:"blocked_reason" gorm:"-"`
}

// AccessReview lists unresolved trusted inbox applications, never the supplied invitation or its hash.
type AccessReview struct {
	TelegramUsername string     `json:"telegram_username"`
	ID               uint       `json:"id,string"`
	BotID            uint       `json:"bot_id,string"`
	ChatID           int64      `json:"chat_id,string"`
	TelegramID       int64      `json:"telegram_id,string"`
	InviteID         uint       `json:"invite_id,string"`
	State            string     `json:"state"`
	ReviewReason     string     `json:"review_reason"`
	ActorAdminID     uint       `json:"actor_admin_id,string"`
	ReviewedAt       *time.Time `json:"reviewed_at,omitempty"`
	ReceivedAt       time.Time  `json:"received_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// AccessRecoveryInput binds an authorized action to the exact operation and route versions.
// refresh_member queues only a historical-identity read; granting access requires a separate retry.
// Reason is a fixed review code, not free text that might contain invitation secrets.
type AccessRecoveryInput struct {
	ExpectedVersion      uint64 `json:"expected_version,string"`
	ExpectedRouteVersion uint64 `json:"expected_route_version,string"`
	Action               string `json:"action"`
	Reason               string `json:"reason"`
}

// AccessAdminRepository shares the existing durable work queue; writes never call Telegram inline.
type AccessAdminRepository interface {
	AccessTask(context.Context, uint, uint) (*AccessTask, error)
	ListAccessTasks(context.Context, uint, uint, int) ([]AccessTask, error)
	ListAccessReviews(context.Context, uint, uint, int) ([]AccessReview, error)
	RecoverAccess(context.Context, uint, uint, uint, AccessRecoveryInput, *AccessRoute, time.Time) error
}

// AccessAdmin exposes only bounded queries and versioned, audited recovery to authenticated administrators.
type AccessAdmin interface {
	ListAccessTasks(context.Context, uint, uint, int) ([]AccessTask, error)
	ListAccessReviews(context.Context, uint, uint, int) ([]AccessReview, error)
	RecoverAccess(context.Context, uint, uint, uint, AccessRecoveryInput) error
}

// AccessReviewSearchReader provides bot-scoped numbered application search by identity.
// Keyword follows LifecycleKeyword rules; total counts applications, not distinct members.
type AccessReviewSearchReader interface {
	SearchAccessReviewsPage(context.Context, uint, int, int, string, ...string) ([]AccessReview, int64, error)
}

// AccessReviewFilter validates one optional review status; omission retains legacy all-row reads.
// Returns pending, reviewed or all; unknown/repeated values fail before storage access.
func AccessReviewFilter(status ...string) (string, error) {
	if len(status) == 0 {
		return "all", nil
	}
	if len(status) != 1 {
		return "", ErrInvalid
	}
	switch status[0] {
	case "pending", "reviewed", "all":
		return status[0], nil
	default:
		return "", ErrInvalid
	}
}

// AccessPageReader adds optional numbered reads without changing cursor consumers.
// bot scopes all rows; page is one-based and size is bounded to 100. Total is the filtered COUNT.
type AccessPageReader interface {
	AccessTasksPage(context.Context, uint, int, int, ...string) ([]AccessTask, int64, error)
	AccessReviewsPage(context.Context, uint, int, int) ([]AccessReview, int64, error)
}

// AccessOrderFilter validates one optional exact order number, never a SQL pattern.
// Empty selects all rows; repeated, overlong, whitespace/control values are rejected.
func AccessOrderFilter(orderNo ...string) (string, error) {
	if len(orderNo) > 1 {
		return "", ErrInvalid
	}
	if len(orderNo) == 0 {
		return "", nil
	}
	value := orderNo[0]
	if len(value) > 64 || !utf8.ValidString(value) {
		return "", ErrInvalid
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", ErrInvalid
		}
	}
	return value, nil
}

// LifecyclePageReader reads only lifecycle tasks in the selected bot scope.
// Optional status filters the persisted member task state, not invitation cleanup.
type LifecyclePageReader interface {
	LifecycleTasksPage(context.Context, uint, int, int, ...string) ([]LifecycleTask, int64, error)
}

// LifecycleUserTasks keeps complete per-bot history for one Telegram identity.
// TelegramID is encoded as a decimal string to preserve IDs beyond JavaScript precision.
type LifecycleUserTasks struct {
	TelegramID       int64           `json:"telegram_id,string"`
	TelegramUsername string          `json:"telegram_username"`
	Tasks            []LifecycleTask `json:"tasks"`
}

// LifecycleUserPageReader pages distinct Telegram identities in ascending numeric order.
// Status selects identities; their returned tasks include every state and historical chat task.
type LifecycleUserPageReader interface {
	LifecycleUsersPage(context.Context, uint, int, int, ...string) ([]LifecycleUserTasks, int64, error)
}

// LifecycleUserSearchReader extends grouped reads without changing existing status-only callers.
// Status and keyword select users, never truncate their task history.
type LifecycleUserSearchReader interface {
	SearchLifecycleUsersPage(context.Context, uint, int, int, string, string) ([]LifecycleUserTasks, int64, error)
}

// LifecycleKeyword validates an optional keyword and returns normalized text plus an exact ID.
// ASCII username fragments allow one leading @ and literal underscores; SQL wildcards are rejected.
// Empty trimmed input disables filtering. Numeric input must fit a positive signed Telegram ID.
func LifecycleKeyword(input string) (string, int64, error) {
	if len(input) > 128 || !utf8.ValidString(input) {
		return "", 0, ErrInvalid
	}
	value := strings.TrimSpace(input)
	if value == "" {
		return "", 0, nil
	}
	numeric := true
	for _, character := range value {
		if character < '0' || character > '9' {
			numeric = false
		}
	}
	if numeric {
		identity, err := strconv.ParseInt(value, 10, 64)
		if err != nil || identity <= 0 {
			return "", 0, ErrInvalid
		}
		return strconv.FormatInt(identity, 10), identity, nil
	}
	value = strings.TrimPrefix(value, "@")
	if value == "" || len(value) > 32 {
		return "", 0, ErrInvalid
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			return "", 0, ErrInvalid
		}
	}
	// Preserve @ for numeric username fragments so repeated boundary validation stays idempotent.
	return "@" + strings.ToLower(value), 0, nil
}

// LifecycleFilterStates validates one optional UI status and returns its persisted states.
// Empty status selects all records; unknown or repeated filters return ErrInvalid.
func LifecycleFilterStates(status ...string) ([]string, error) {
	if len(status) > 1 {
		return nil, ErrInvalid
	}
	if len(status) == 0 || status[0] == "" {
		return nil, nil
	}
	switch status[0] {
	case "processing":
		return []string{"pending", "creating", "retryable"}, nil
	case "needs_review":
		return []string{"needs_review", "failed"}, nil
	case "completed":
		return []string{"succeeded"}, nil
	default:
		return nil, ErrInvalid
	}
}

// ValidAdminPage rejects invalid sizes and offsets that cannot fit a signed database integer.
// page/size are caller input; the result indicates whether COUNT/OFFSET may be executed.
func ValidAdminPage(page, size int) bool {
	return page > 0 && size > 0 && size <= 100 && page-1 <= (int(^uint(0)>>1)-size)/size
}

// AttentionSummary is the secret-free cross-bot count returned by optional admin summary reads.
type AttentionSummary struct {
	AttentionCount int64 `json:"attention_count"`
}

// AccessAttentionReader optionally exposes global counts without changing existing list adapters.
type AccessAttentionReader interface {
	AccessAttention(context.Context) (AttentionSummary, error)
	ReviewAttention(context.Context) (AttentionSummary, error)
}

// AccessAttentionRepository reads current task/review anomalies and bounded delivery candidates.
// Candidates contain only IDs for successful create operations on current active invitations;
// callers must exhaust internal pagination before returning a global summary.
type AccessAttentionRepository interface {
	CountAccessAttention(context.Context) (int64, error)
	CountReviewAttention(context.Context) (int64, error)
	AccessDeliveryCandidates(context.Context, uint, int) ([]AccessTask, error)
}

// LifecycleAttentionReader optionally exposes global durable lifecycle anomaly counts.
type LifecycleAttentionReader interface {
	LifecycleAttention(context.Context) (AttentionSummary, error)
}

// IdentityMigrationInput is an administrator-reviewed, read-only request. Decimal strings
// preserve identifiers and versions across JSON clients. ExpectedVersions may be omitted
// for the first snapshot; when supplied it must cover every normalized target exactly.
type IdentityMigrationInput struct {
	AccountID        uint              `json:"account_id,string"`
	OldTelegramID    string            `json:"old_telegram_id"`
	NewTelegramID    string            `json:"new_telegram_id"`
	TargetIDs        []string          `json:"target_ids"`
	ExpectedVersions map[string]string `json:"expected_versions,omitempty"`
}

// IdentityMigrationSource exposes immutable term provenance, never hashes or credentials.
// Revoked and future sources remain visible but do not contribute to effective expiry.
type IdentityMigrationSource struct {
	ID          string    `json:"id"`
	OrderItemID string    `json:"order_item_id"`
	PaidAt      time.Time `json:"paid_at"`
	Kind        string    `json:"kind"`
	Months      int64     `json:"months,string"`
	Revoked     bool      `json:"revoked"`
}

// IdentityMigrationTarget is the original scope calculation and current route/version snapshot.
// A nil expiry with permanent kind means permanent, not an inferred Telegram membership.
type IdentityMigrationTarget struct {
	TargetID           string                    `json:"target_id"`
	EntitlementVersion string                    `json:"entitlement_version"`
	GroupVersion       string                    `json:"group_version"`
	RouteVersion       string                    `json:"route_version"`
	BotID              string                    `json:"bot_id"`
	ChatID             string                    `json:"chat_id"`
	Kind               string                    `json:"kind"`
	ExpiresAt          *time.Time                `json:"expires_at,omitempty"`
	LimitedExpiresAt   *time.Time                `json:"limited_expires_at,omitempty"`
	Sources            []IdentityMigrationSource `json:"sources"`
}

// IdentityMigrationPreview is a point-in-time precheck, not an execution authorization.
// Binding mismatch is an explicit blocker; this interface never changes login binding.
// Database member observations are conflict evidence only, never proof of actual admission.
type IdentityMigrationPreview struct {
	AccountID           uint                      `json:"account_id,string"`
	OldTelegramID       string                    `json:"old_telegram_id"`
	NewTelegramID       string                    `json:"new_telegram_id"`
	ObservedAt          time.Time                 `json:"observed_at"`
	BindingMatchesNewID bool                      `json:"binding_matches_new_id"`
	BindingVersion      string                    `json:"binding_version"`
	Blockers            []string                  `json:"blockers"`
	Targets             []IdentityMigrationTarget `json:"targets"`
}

// IdentityMigrationCommit requires explicit human verification and exact preview versions.
// ActorAdminID must come from the authenticated administrator, never the request body.
type IdentityMigrationCommit struct {
	IdentityMigrationInput
	ActorAdminID           uint              `json:"-"`
	HumanVerified          bool              `json:"human_verified"`
	ExpectedBindingVersion string            `json:"expected_binding_version"`
	ExpectedRoutes         map[string]string `json:"expected_routes"`
	ExpectedGroups         map[string]string `json:"expected_groups"`
}

type IdentityMigrationWork struct {
	ID            uint
	TargetID      uint
	BotID         uint
	ChatID        int64
	RouteVersion  uint64
	OldTelegramID int64
	NewTelegramID int64
}

// IdentityMigrationObserver enumerates only explicitly reviewed pending scopes for trusted reads.
type IdentityMigrationObserver interface {
	PendingIdentityMigrations(context.Context, uint, uint, int) ([]IdentityMigrationWork, error)
	ObserveAccess(context.Context, AccessObservation) (bool, error)
}

// IdentityMigrationWriter commits only reviewed mappings; it never calls Telegram or changes bindings.
type IdentityMigrationWriter interface {
	CommitIdentityMigration(context.Context, IdentityMigrationCommit, time.Time) error
}

// IdentityMigrationReader reads a consistent snapshot at trusted server time; no writes,
// Telegram calls, migration execution, or administrator authorization are provided here.
type IdentityMigrationReader interface {
	PreviewIdentityMigration(context.Context, IdentityMigrationInput, time.Time) (*IdentityMigrationPreview, error)
}
