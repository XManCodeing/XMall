package telegramapi

import "encoding/json"

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Duration     int    `json:"duration"`
	Performer    string `json:"performer,omitempty"`
	Title        string `json:"title,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type VideoNote struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Length       int    `json:"length"`
	Duration     int    `json:"duration"`
	FileSize     int64  `json:"file_size,omitempty"`
}
type Sticker struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Type         string `json:"type"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	IsAnimated   bool   `json:"is_animated"`
	IsVideo      bool   `json:"is_video"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// ChatMember represents all status variants. Permission flags are optional so
// absent permissions and explicit denials survive decoding and persistence.
type ChatMember struct {
	User        User            `json:"user"`
	Status      string          `json:"status"`
	UntilDate   int64           `json:"until_date,omitempty"`
	IsMember    bool            `json:"is_member,omitempty"`
	IsAnonymous bool            `json:"is_anonymous,omitempty"`
	CustomTitle string          `json:"custom_title,omitempty"`
	Permissions map[string]bool `json:"-"`
}

func (m *ChatMember) UnmarshalJSON(data []byte) error {
	type plain ChatMember
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, raw := range fields {
		if len(key) > 4 && key[:4] == "can_" {
			var allowed bool
			if err := json.Unmarshal(raw, &allowed); err != nil {
				return err
			}
			if value.Permissions == nil {
				value.Permissions = map[string]bool{}
			}
			value.Permissions[key] = allowed
		}
	}
	*m = ChatMember(value)
	return nil
}
func (m ChatMember) MarshalJSON() ([]byte, error) {
	type plain ChatMember
	data, err := json.Marshal(plain(m))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range m.Permissions {
		if len(key) > 4 && key[:4] == "can_" {
			fields[key], _ = json.Marshal(value)
		}
	}
	return json.Marshal(fields)
}

type ChatInviteLink struct {
	InviteLink              string `json:"invite_link"`
	Creator                 User   `json:"creator"`
	CreatesJoinRequest      bool   `json:"creates_join_request"`
	IsPrimary               bool   `json:"is_primary"`
	IsRevoked               bool   `json:"is_revoked"`
	Name                    string `json:"name,omitempty"`
	ExpireDate              int64  `json:"expire_date,omitempty"`
	MemberLimit             int    `json:"member_limit,omitempty"`
	PendingJoinRequestCount int    `json:"pending_join_request_count,omitempty"`
}
type ChatMemberUpdated struct {
	Chat                    Chat            `json:"chat"`
	From                    User            `json:"from"`
	Date                    int64           `json:"date"`
	OldChatMember           ChatMember      `json:"old_chat_member"`
	NewChatMember           ChatMember      `json:"new_chat_member"`
	InviteLink              *ChatInviteLink `json:"invite_link,omitempty"`
	ViaJoinRequest          bool            `json:"via_join_request,omitempty"`
	ViaChatFolderInviteLink bool            `json:"via_chat_folder_invite_link,omitempty"`
}
type ChatJoinRequest struct {
	Chat       Chat            `json:"chat"`
	From       User            `json:"from"`
	UserChatID int64           `json:"user_chat_id"`
	Date       int64           `json:"date"`
	Bio        string          `json:"bio,omitempty"`
	InviteLink *ChatInviteLink `json:"invite_link,omitempty"`
}
