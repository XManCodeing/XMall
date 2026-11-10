package application

import (
	"context"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// ExistingMembershipInvite reads the current entry without creating an invitation or operation.
// identity must be authenticated; target is a logical membership group. Missing entries return
// ErrNotFound, while unavailable entries expose their state only, never invitation secrets.
func (service *AccessService) ExistingMembershipInvite(ctx context.Context, identity c.AccessIdentity, target uint) (*c.AccessEntry, error) {
	if target == 0 {
		return nil, c.ErrInvalid
	}
	rows, err := service.ListMemberships(ctx, identity, target-1, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].TargetID != target {
		return nil, c.ErrNotFound
	}
	entry := &c.AccessEntry{AccessGroup: rows[0]}
	if !entry.Active || entry.InviteID == 0 || entry.InviteState != "active" {
		return entry, nil
	}
	before, err := service.PrepareInviteDelivery(ctx, identity, entry.InviteID)
	if err != nil {
		return nil, err
	}
	invite, err := service.Store.Invite(ctx, entry.InviteID)
	if err != nil {
		return nil, err
	}
	if invite == nil || invite.TargetID != target {
		return nil, c.ErrUntrustedIdentity
	}
	link, err := service.Cipher.Open(invite.LinkCipher)
	if err != nil {
		return nil, c.ErrCapability
	}
	if !validAccessLink(link) || accessHash(link) != invite.LinkHash {
		return nil, c.ErrUntrustedIdentity
	}
	// Recheck after reading/decrypting: a concurrent binding, route or entitlement
	// change cannot reuse the first authorization snapshot to disclose the URL.
	after, err := service.PrepareInviteDelivery(ctx, identity, entry.InviteID)
	if err != nil {
		return nil, err
	}
	if *before != *after {
		return nil, c.ErrConflict
	}
	entry.Link = link
	return entry, nil
}
