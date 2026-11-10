package domain

// MissingCapabilities returns the purpose-specific permissions absent from a
// persisted observation. Callers separately validate freshness and route identity.
func MissingCapabilities(target Target) []string {
	missing := []string{}
	observation := target.Observation
	if observation.ChatType != "supergroup" {
		missing = append(missing, "supergroup")
	}
	if observation.MemberStatus != "administrator" && observation.MemberStatus != "creator" {
		missing = append(missing, "administrator")
	}
	if target.Purpose == "support" {
		if !observation.IsForum {
			missing = append(missing, "forum")
		}
		if !observation.CanManageTopics {
			missing = append(missing, "can_manage_topics")
		}
	}
	if target.Purpose == "membership" {
		if !observation.CanInviteUsers {
			missing = append(missing, "can_invite_users")
		}
		if !observation.CanRestrictMembers {
			missing = append(missing, "can_restrict_members")
		}
	}
	return missing
}
