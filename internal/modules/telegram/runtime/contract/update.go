package contract

// Update is the persisted runtime envelope. Existing JSON keys remain unchanged.
// Reply and sender-chat data are observations, never proof of authorization.
type Update struct {
	// Actor contains only the current event's native Telegram sender, never a reply/forward author.
	Actor               *UserReference     `json:"actor,omitempty"`
	TopicService        *TopicServiceEvent `json:"topic_service,omitempty"`
	HasProtectedContent bool               `json:"has_protected_content,omitempty"`
	ID                  int64              `json:"id"`
	UserID              int64              `json:"user_id"`
	ChatID              int64              `json:"chat_id"`
	Private             bool               `json:"private"`
	Text                string             `json:"text"`
	MessageID           int64              `json:"message_id,omitempty"`
	MessageKind         string             `json:"message_kind,omitempty"`
	CallbackID          string             `json:"callback_id,omitempty"`
	CallbackData        string             `json:"callback_data,omitempty"`
	Locale              string             `json:"locale,omitempty"`
	FirstName           string             `json:"first_name,omitempty"`
	LastName            string             `json:"last_name,omitempty"`
	Username            string             `json:"username,omitempty"`
	Type                string             `json:"type,omitempty"`
	ChatType            string             `json:"chat_type,omitempty"`
	ThreadID            int64              `json:"thread_id,omitempty"`
	IsTopicMessage      bool               `json:"is_topic_message,omitempty"`
	Reply               *ReplyReference    `json:"reply,omitempty"`
	SenderChat          *ChatReference     `json:"sender_chat,omitempty"`
	Caption             string             `json:"caption,omitempty"`
	Entities            []MessageEntity    `json:"entities,omitempty"`
	CaptionEntities     []MessageEntity    `json:"caption_entities,omitempty"`
	Media               []Media            `json:"media,omitempty"`
	MediaGroupID        string             `json:"media_group_id,omitempty"`
	Member              *MemberUpdate      `json:"member,omitempty"`
	JoinRequest         *JoinRequest       `json:"join_request,omitempty"`
	Migration           *ChatMigration     `json:"migration,omitempty"`
}

// TopicServiceEvent records only native closed/reopened service observations.
// Date is the platform message timestamp, used conservatively against local
// operation barriers; neither text nor a topic title can manufacture an event.
type TopicServiceEvent struct {
	State string `json:"state"` // closed or open
	Date  int64  `json:"date"`
}

type UserReference struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot,omitempty"`
	FirstName    string `json:"first_name,omitempty"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}
type ChatReference struct {
	ID       int64  `json:"id"`
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	IsForum  bool   `json:"is_forum,omitempty"`
}

// ReplyReference must be resolved against a server-side message mapping before use.
// It deliberately has no Trusted flag: receiving a native reply grants no authority.
type ReplyReference struct {
	ChatID     int64          `json:"chat_id"`
	MessageID  int64          `json:"message_id"`
	ThreadID   int64          `json:"thread_id,omitempty"`
	UserID     int64          `json:"user_id,omitempty"`
	SenderChat *ChatReference `json:"sender_chat,omitempty"`
}
type MessageEntity struct {
	Type          string         `json:"type"`
	Offset        int            `json:"offset"` // UTF-16 code units
	Length        int            `json:"length"`
	URL           string         `json:"url,omitempty"`
	User          *UserReference `json:"user,omitempty"`
	Language      string         `json:"language,omitempty"`
	CustomEmojiID string         `json:"custom_emoji_id,omitempty"`
}

// Media contains reusable file references, never downloaded bytes. Photos retain all sizes.
type Media struct {
	Type         string `json:"type"`
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
}
type ChatMember struct {
	User        UserReference `json:"user"`
	Status      string        `json:"status"`
	UntilDate   int64         `json:"until_date,omitempty"`
	IsMember    bool          `json:"is_member,omitempty"`
	IsAnonymous bool          `json:"is_anonymous,omitempty"`
	CustomTitle string        `json:"custom_title,omitempty"`
	// Permissions retain optional platform permission flags, including explicit false.
	Permissions map[string]bool `json:"permissions,omitempty"`
}
type InviteLink struct {
	InviteLink              string        `json:"invite_link"`
	Creator                 UserReference `json:"creator"`
	CreatesJoinRequest      bool          `json:"creates_join_request"`
	IsPrimary               bool          `json:"is_primary"`
	IsRevoked               bool          `json:"is_revoked"`
	Name                    string        `json:"name,omitempty"`
	ExpireDate              int64         `json:"expire_date,omitempty"`
	MemberLimit             int           `json:"member_limit,omitempty"`
	PendingJoinRequestCount int           `json:"pending_join_request_count,omitempty"`
}
type MemberUpdate struct {
	Operator                UserReference `json:"operator"`
	Subject                 UserReference `json:"subject"`
	Date                    int64         `json:"date"`
	Old                     ChatMember    `json:"old"`
	New                     ChatMember    `json:"new"`
	InviteLink              *InviteLink   `json:"invite_link,omitempty"`
	ViaJoinRequest          bool          `json:"via_join_request,omitempty"`
	ViaChatFolderInviteLink bool          `json:"via_chat_folder_invite_link,omitempty"`
}
type JoinRequest struct {
	User       UserReference `json:"user"`
	UserChatID int64         `json:"user_chat_id"` // temporary reachability, not a permanent private chat route
	Date       int64         `json:"date"`
	Bio        string        `json:"bio,omitempty"`
	InviteLink *InviteLink   `json:"invite_link,omitempty"`
}
type ChatMigration struct {
	FromChatID int64 `json:"from_chat_id"`
	ToChatID   int64 `json:"to_chat_id"`
}
