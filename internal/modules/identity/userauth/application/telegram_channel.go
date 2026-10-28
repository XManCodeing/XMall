package application

import (
	"context"
	userauthdomain "github.com/XManCodeing/XMall/internal/modules/identity/userauth/domain"
	"strings"
	"time"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	telegramauthapp "github.com/XManCodeing/XMall/internal/modules/identity/telegramauth/application"
	"github.com/XManCodeing/XMall/internal/telegramidentity"
)

// TelegramChannelIdentityInput Telegram 渠道身份输入
type TelegramChannelIdentityInput struct {
	ChannelUserID string
	Username      string
	FirstName     string
	LastName      string
	AvatarURL     string
	// InviteCode 邀请注册模式下 Bot 新用户建号所需的邀请码。
	InviteCode string
}

// BindTelegramChannelByEmailCodeInput Telegram 渠道邮箱验证码绑定输入
type BindTelegramChannelByEmailCodeInput struct {
	Identity TelegramChannelIdentityInput
	Email    string
	Code     string
}

// ResolveTelegramChannelIdentity 解析 Telegram 渠道身份
func (s *Service) ResolveTelegramChannelIdentity(input TelegramChannelIdentityInput) (*userdomain.User, *externalidentitydomain.Identity, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input)
	if err != nil {
		return nil, nil, err
	}
	return s.resolveTelegramChannelIdentity(verified)
}

// ProvisionTelegramChannelIdentity 预置 Telegram 渠道身份
func (s *Service) ProvisionTelegramChannelIdentity(input TelegramChannelIdentityInput) (*userdomain.User, *externalidentitydomain.Identity, bool, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input)
	if err != nil {
		return nil, nil, false, err
	}
	return s.provisionTelegramChannelIdentity(verified, input.InviteCode)
}

// BindTelegramChannelByEmailCode 使用邮箱验证码绑定 Telegram 渠道身份到既有账号
func (s *Service) BindTelegramChannelByEmailCode(input BindTelegramChannelByEmailCodeInput) (*userdomain.User, *externalidentitydomain.Identity, uint, error) {
	return s.bindTelegramChannelByEmailCode(0, input)
}

// BindTelegramBotByEmailCode accepts identities obtained from authenticated Bot
// updates only. Bot ID must be the stable configured instance, not user input.
func (s *Service) BindTelegramBotByEmailCode(botID uint, input BindTelegramChannelByEmailCodeInput) (*userdomain.User, *externalidentitydomain.Identity, uint, error) {
	if botID == 0 {
		return nil, nil, 0, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	return s.bindTelegramChannelByEmailCode(botID, input)
}

func (s *Service) bindTelegramChannelByEmailCode(botID uint, input BindTelegramChannelByEmailCodeInput) (*userdomain.User, *externalidentitydomain.Identity, uint, error) {
	verified, err := normalizeTelegramChannelIdentityInput(input.Identity)
	if err != nil {
		return nil, nil, 0, err
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil || s.codeRepo == nil {
		return nil, nil, 0, telegramauthapp.ErrTelegramAuthConfigInvalid
	}

	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, nil, 0, err
	}
	if _, err := s.verifyCode(email, constants.VerifyPurposeTelegramBind, input.Code); err != nil {
		return nil, nil, 0, err
	}

	targetUser, err := s.userRepo.GetByEmail(email)
	if err != nil {
		return nil, nil, 0, err
	}
	if targetUser == nil {
		return nil, nil, 0, ErrNotFound
	}
	if strings.ToLower(strings.TrimSpace(targetUser.Status)) != constants.UserStatusActive {
		return nil, nil, 0, ErrUserDisabled
	}

	return s.bindTelegramIdentityToUser(botID, targetUser, verified)
}

func (s *Service) resolveTelegramChannelIdentity(verified *telegramauthapp.IdentityVerified) (*userdomain.User, *externalidentitydomain.Identity, error) {
	if verified == nil {
		return nil, nil, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil {
		return nil, nil, telegramauthapp.ErrTelegramAuthConfigInvalid
	}

	if s.authUnitOfWork == nil {
		return nil, nil, telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	var user *userdomain.User
	var identity *externalidentitydomain.Identity
	err := s.authUnitOfWork.WithinTransaction(context.Background(), func(tx AuthTransaction) error {
		var err error
		identity, err = tx.GetIdentityByProviderUserID(verified.Provider, verified.ProviderUserID)
		if err != nil || identity == nil {
			return err
		}
		user, err = activeTransactionUser(tx, identity.UserID)
		if err != nil {
			return err
		}
		if applyTelegramIdentity(verified, identity) {
			identity.UpdatedAt = time.Now()
			return tx.UpdateIdentity(identity)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return user, identity, nil
}

func (s *Service) provisionTelegramChannelIdentity(verified *telegramauthapp.IdentityVerified, inviteCode string) (*userdomain.User, *externalidentitydomain.Identity, bool, error) {
	if verified == nil {
		return nil, nil, false, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	if s.userOAuthIdentityRepo == nil || s.userRepo == nil {
		return nil, nil, false, telegramauthapp.ErrTelegramAuthConfigInvalid
	}

	user, identity, err := s.resolveTelegramChannelIdentity(verified)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		return user, identity, false, nil
	}

	placeholderUser, err := s.userRepo.GetByEmail(telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID))
	if err != nil {
		return nil, nil, false, err
	}
	created := placeholderUser == nil

	user, err = s.findOrCreateTelegramUser(verified, inviteCode)
	if err != nil {
		return nil, nil, false, err
	}

	identity, err = s.userOAuthIdentityRepo.GetByUserProvider(user.ID, verified.Provider)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		if identity.ProviderUserID != verified.ProviderUserID {
			return nil, nil, false, ErrUserOAuthAlreadyBound
		}
		if applyTelegramIdentity(verified, identity) {
			identity.UpdatedAt = time.Now()
			if err := s.updateTelegramIdentity(identity); err != nil {
				return nil, nil, false, err
			}
		}
		return user, identity, created, nil
	}

	identity = &externalidentitydomain.Identity{
		UserID:         user.ID,
		Provider:       verified.Provider,
		ProviderUserID: verified.ProviderUserID,
		Username:       verified.Username,
		AvatarURL:      verified.AvatarURL,
		AuthAt:         &verified.AuthAt,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := s.userOAuthIdentityRepo.Create(identity); err != nil {
		existing, getErr := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
		if getErr != nil {
			return nil, nil, false, err
		}
		if existing == nil {
			return nil, nil, false, err
		}
		identity = existing
		user, err = s.getActiveUserByID(existing.UserID)
		if err != nil {
			return nil, nil, false, err
		}
		return user, identity, false, nil
	}

	return user, identity, created, nil
}

func (s *Service) bindTelegramIdentityToUser(botID uint, targetUser *userdomain.User, verified *telegramauthapp.IdentityVerified) (user *userdomain.User, identity *externalidentitydomain.Identity, previousUserID uint, err error) {
	if targetUser == nil || verified == nil {
		return nil, nil, 0, ErrNotFound
	}
	if s.authUnitOfWork == nil {
		return nil, nil, 0, telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	err = s.authUnitOfWork.WithinTransaction(context.Background(), func(tx AuthTransaction) error {
		// Reset callback outputs because the unit of work can retry deadlocks.
		user, identity, previousUserID = nil, nil, 0
		lockedTarget, txErr := activeTransactionUser(tx, targetUser.ID)
		if txErr != nil {
			return txErr
		}
		if lockedTarget.Email != targetUser.Email {
			return ErrNotFound
		}
		current, txErr := tx.GetIdentityByUserProvider(lockedTarget.ID, verified.Provider)
		if txErr != nil {
			return txErr
		}
		if current != nil && current.ProviderUserID != verified.ProviderUserID {
			return ErrUserOAuthAlreadyBound
		}
		occupied, txErr := tx.GetIdentityByProviderUserID(verified.Provider, verified.ProviderUserID)
		if txErr != nil {
			return txErr
		}
		now := time.Now()
		if occupied != nil && occupied.UserID != lockedTarget.ID {
			previous, lookupErr := activeTransactionUser(tx, occupied.UserID)
			if lookupErr != nil {
				return lookupErr
			}
			// Derive B exclusively from the verified identity. A generic placeholder
			// address alone is insufficient: it must belong to this Telegram ID.
			if previous.Email != telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID) || !previous.PasswordSetupRequired {
				return ErrUserOAuthIdentityExists
			}
			previousUserID = previous.ID
			// A transfer starts a new binding lifetime. Never reuse the old
			// identity ID: queued actions from B must fail even after a rebind.
			if txErr := tx.RevokeTelegramHistoryGrants(occupied.ID, now); txErr != nil {
				return txErr
			}
			if txErr := tx.DeleteIdentityByID(occupied.ID); txErr != nil {
				return txErr
			}
			occupied.ID = 0
			occupied.UserID = lockedTarget.ID
			occupied.CreatedAt = now
			if txErr := tx.CreateIdentity(occupied); txErr != nil {
				return txErr
			}
			if botID != 0 {
				grant := &userauthdomain.TelegramHistoryGrant{BotID: botID, TelegramUserID: verified.ProviderUserID, PreviousUserID: previous.ID, CurrentUserID: lockedTarget.ID, IdentityID: occupied.ID, Status: userauthdomain.TelegramHistoryActive, CreatedAt: now, UpdatedAt: now}
				if txErr := tx.CreateTelegramHistoryGrant(grant); txErr != nil {
					return txErr
				}
			}
		}
		if occupied == nil {
			occupied = &externalidentitydomain.Identity{UserID: lockedTarget.ID, Provider: verified.Provider, ProviderUserID: verified.ProviderUserID, CreatedAt: now}
			applyTelegramIdentity(verified, occupied)
			occupied.UpdatedAt = now
			if txErr := tx.CreateIdentity(occupied); txErr != nil {
				return txErr
			}
		} else {
			applyTelegramIdentity(verified, occupied)
			occupied.UpdatedAt = now
			if txErr := tx.UpdateIdentity(occupied); txErr != nil {
				return txErr
			}
		}
		user, identity = lockedTarget, occupied
		return nil
	})
	if err != nil {
		return nil, nil, 0, err
	}
	return user, identity, previousUserID, nil
}

func normalizeTelegramChannelIdentityInput(input TelegramChannelIdentityInput) (*telegramauthapp.IdentityVerified, error) {
	providerUserID := strings.TrimSpace(input.ChannelUserID)
	if providerUserID == "" {
		return nil, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	return &telegramauthapp.IdentityVerified{
		Provider:       constants.UserOAuthProviderTelegram,
		ProviderUserID: providerUserID,
		Username:       strings.TrimSpace(input.Username),
		AvatarURL:      strings.TrimSpace(input.AvatarURL),
		FirstName:      strings.TrimSpace(input.FirstName),
		LastName:       strings.TrimSpace(input.LastName),
		AuthAt:         time.Now(),
	}, nil
}

// AllowedTelegramHistoryUserIDs returns historical owners only (not the current
// account). Call on every read using the authenticated Bot update's Telegram ID
// and freshly resolved current user. These IDs must NEVER authorize mutations.
func (s *Service) AllowedTelegramHistoryUserIDs(ctx context.Context, botID uint, telegramUserID string, currentUserID uint) ([]uint, error) {
	telegramUserID = strings.TrimSpace(telegramUserID)
	if botID == 0 || telegramUserID == "" || currentUserID == 0 {
		return nil, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	if s == nil || s.authUnitOfWork == nil {
		return nil, telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	var ids []uint
	err := s.authUnitOfWork.WithinTransaction(ctx, func(tx AuthTransaction) error {
		ids = nil
		if _, err := activeTransactionUser(tx, currentUserID); err != nil {
			return err
		}
		identity, err := tx.GetIdentityByProviderUserID(constants.UserOAuthProviderTelegram, telegramUserID)
		if err != nil {
			return err
		}
		if identity == nil || identity.UserID != currentUserID {
			return ErrUserOAuthNotBound
		}
		grants, err := tx.ListTelegramHistoryGrants(botID, telegramUserID, currentUserID, identity.ID)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			previous, lookupErr := tx.GetUserByIDForUpdate(grant.PreviousUserID)
			if lookupErr != nil {
				return lookupErr
			}
			if previous == nil || strings.ToLower(strings.TrimSpace(previous.Status)) != constants.UserStatusActive {
				continue
			}
			ids = append(ids, grant.PreviousUserID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CanReadTelegramHistory checks a single historical owner; current-account
// authorization stays with the normal account/ownership checks.
func (s *Service) CanReadTelegramHistory(ctx context.Context, botID uint, telegramUserID string, currentUserID, historyUserID uint) (bool, error) {
	ids, err := s.AllowedTelegramHistoryUserIDs(ctx, botID, telegramUserID, currentUserID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == historyUserID {
			return true, nil
		}
	}
	return false, nil
}

// TelegramIdentityStamp identifies a single binding lifetime. Persist it with
// sessions and queued deliveries; do not reconstruct it at delivery time.
type TelegramIdentityStamp struct {
	BotID          uint
	TelegramUserID string
	UserID         uint
	IdentityID     uint
}

// ValidateTelegramIdentityStamp is a point-in-time check, not a lock spanning
// HTTP or another service. Mutations must validate inside their own transaction.
func (s *Service) ValidateTelegramIdentityStamp(ctx context.Context, stamp TelegramIdentityStamp) error {
	if stamp.BotID == 0 || stamp.UserID == 0 || stamp.IdentityID == 0 || strings.TrimSpace(stamp.TelegramUserID) == "" {
		return telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	if s == nil || s.authUnitOfWork == nil {
		return telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	return s.authUnitOfWork.WithinTransaction(ctx, func(tx AuthTransaction) error {
		if _, err := activeTransactionUser(tx, stamp.UserID); err != nil {
			return err
		}
		identity, err := tx.GetIdentityByProviderUserID(constants.UserOAuthProviderTelegram, stamp.TelegramUserID)
		if err != nil {
			return err
		}
		if identity == nil || identity.ID != stamp.IdentityID || identity.UserID != stamp.UserID {
			return ErrUserOAuthNotBound
		}
		return nil
	})
}
