package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"

	"github.com/XManCodeing/XMall/internal/constants"
	externalidentitydomain "github.com/XManCodeing/XMall/internal/modules/identity/externalidentity/domain"
	telegramauthapp "github.com/XManCodeing/XMall/internal/modules/identity/telegramauth/application"
	"github.com/XManCodeing/XMall/internal/telegramidentity"

	"golang.org/x/crypto/bcrypt"
)

func (s *Service) getActiveUserByID(userID uint) (*userdomain.User, error) {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	if strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
		return nil, ErrUserDisabled
	}
	return user, nil
}

func (s *Service) findOrCreateTelegramUser(verified *telegramauthapp.IdentityVerified, inviteCode string) (*userdomain.User, error) {
	if verified == nil {
		return nil, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	email := telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID)
	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		return nil, err
	}
	if user != nil {
		if strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
			return nil, ErrUserDisabled
		}
		return user, nil
	}
	policy, err := s.loadRegistrationPolicy(inviteCode)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, ErrRegistrationDisabled
	}
	if err := s.precheckInviteCode(policy); err != nil {
		return nil, err
	}

	user, err = newTelegramPlaceholderUser(verified)
	if err != nil {
		return nil, err
	}
	if err := s.createUserInTransaction(context.Background(), user, policy); err != nil {
		return nil, err
	}
	s.assignTelegramDefaultLevel(user)
	return user, nil
}

// ProvisionTelegramBotIdentity resolves or provisions a trusted built-in Bot identity.
// ctx controls persistence; botID must come from the authenticated managed Bot,
// never HTTP/user input. input contains the verified Telegram identity and invite.
// Returns user, binding, whether an account was created, and any policy/store error.
// This entry point must not be exposed by an HTTP handler.
func (s *Service) ProvisionTelegramBotIdentity(ctx context.Context, botID uint, input TelegramChannelIdentityInput) (*userdomain.User, *externalidentitydomain.Identity, bool, error) {
	if botID == 0 || ctx == nil {
		return nil, nil, false, telegramauthapp.ErrTelegramAuthPayloadInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	if s == nil || s.authUnitOfWork == nil || s.userRepo == nil || s.userOAuthIdentityRepo == nil {
		return nil, nil, false, telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	verified, err := normalizeTelegramChannelIdentityInput(input)
	if err != nil {
		return nil, nil, false, err
	}
	user, identity, err := s.resolveTelegramChannelIdentity(verified)
	if err != nil || identity != nil {
		return user, identity, false, err
	}
	// Read all settings before entering SQLite's single-connection transaction.
	if s.settingService == nil {
		return nil, nil, false, ErrRegistrationDisabled
	}
	config, err := s.settingService.GetTelegramBotConfig()
	if err != nil {
		return nil, nil, false, err
	}
	if !config.Registration.Enabled {
		return nil, nil, false, ErrRegistrationDisabled
	}
	policy, err := s.loadRegistrationPolicy(input.InviteCode)
	if err != nil {
		return nil, nil, false, err
	}
	policy.Enabled = config.Registration.Enabled // Only override the website switch.
	candidate, err := newTelegramPlaceholderUser(verified)
	if err != nil {
		return nil, nil, false, err
	}
	var created bool
	err = s.authUnitOfWork.WithinTransaction(ctx, func(tx AuthTransaction) error {
		// Unit-of-work retries must not reuse IDs or outputs from rolled-back attempts.
		user, identity, created = nil, nil, false
		fresh := *candidate
		var txErr error
		user, identity, created, txErr = provisionTelegramBotTransaction(tx, verified, &fresh, policy)
		return txErr
	})
	if err != nil {
		// Unique constraints arbitrate concurrent creation across processes. The
		// losing transaction rolls back both account and invite consumption.
		existingUser, existingIdentity, lookupErr := s.resolveTelegramChannelIdentity(verified)
		if lookupErr == nil && existingIdentity != nil {
			return existingUser, existingIdentity, false, nil
		}
		return nil, nil, false, err
	}
	if created {
		s.assignTelegramDefaultLevel(user)
	}
	return user, identity, created, nil
}

// provisionTelegramBotTransaction uses only tx to create/reuse candidate and bind
// verified. policy is an externally loaded snapshot. Returns account, identity,
// creation status, or an error which rolls back the entire mutation.
func provisionTelegramBotTransaction(tx AuthTransaction, verified *telegramauthapp.IdentityVerified, candidate *userdomain.User, policy registrationPolicy) (*userdomain.User, *externalidentitydomain.Identity, bool, error) {
	identity, err := tx.GetIdentityByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		user, err := activeTransactionUser(tx, identity.UserID)
		return user, identity, false, err
	}
	user, err := tx.GetUserByEmail(candidate.Email)
	if err != nil {
		return nil, nil, false, err
	}
	created := user == nil
	if created {
		user = candidate
		if err := createRegisteredUser(tx, user, policy); err != nil {
			return nil, nil, false, err
		}
	} else if strings.ToLower(strings.TrimSpace(user.Status)) != constants.UserStatusActive {
		return nil, nil, false, ErrUserDisabled
	}
	identity, err = tx.GetIdentityByUserProvider(user.ID, verified.Provider)
	if err != nil {
		return nil, nil, false, err
	}
	if identity != nil {
		if identity.ProviderUserID != verified.ProviderUserID {
			return nil, nil, false, ErrUserOAuthAlreadyBound
		}
		return user, identity, false, nil
	}
	identity = &externalidentitydomain.Identity{UserID: user.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	applyTelegramIdentity(verified, identity)
	if err := tx.CreateIdentity(identity); err != nil {
		return nil, nil, false, err
	}
	return user, identity, created, nil
}

// newTelegramPlaceholderUser builds an unsaved account from a verified identity;
// returns the account or a password generation error. No persistence occurs here.
func newTelegramPlaceholderUser(verified *telegramauthapp.IdentityVerified) (*userdomain.User, error) {
	randomSuffix, err := randomNumericCode(16)
	if err != nil {
		return nil, err
	}
	passwordSeed := fmt.Sprintf("tg_%s_%s", verified.ProviderUserID, randomSuffix)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(passwordSeed), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	user := &userdomain.User{
		Email:                 telegramidentity.BuildPlaceholderEmail(verified.ProviderUserID),
		PasswordHash:          string(hashedPassword),
		PasswordSetupRequired: true,
		DisplayName:           userdomain.SanitizeDisplayName(telegramidentity.ResolveDisplayName(verified.ProviderUserID, verified.Username, verified.FirstName, verified.LastName)),
		Status:                constants.UserStatusActive,
		LastLoginAt:           &now,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	return user, nil
}

// assignTelegramDefaultLevel applies the existing default level policy to user
// after commit and refreshes its in-memory level. It returns no value.
func (s *Service) assignTelegramDefaultLevel(user *userdomain.User) {
	// 分配默认会员等级
	if s.memberLevelSvc != nil {
		_ = s.memberLevelSvc.AssignDefaultLevel(user.ID)
		// 同步内存对象的等级，避免调用方后续 Update(Save) 用零值覆盖数据库
		if refreshed, err := s.userRepo.GetByID(user.ID); err == nil && refreshed != nil {
			user.MemberLevelID = refreshed.MemberLevelID
		}
	}
}

// getTelegramIdentityByVerifiedID 按 Telegram 数字 ID 查询绑定，未命中时兼容历史 OIDC subject 绑定。
func (s *Service) getTelegramIdentityByVerifiedID(verified *telegramauthapp.IdentityVerified) (*externalidentitydomain.Identity, error) {
	if verified == nil || s.userOAuthIdentityRepo == nil {
		return nil, telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	identity, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil || identity != nil {
		return identity, err
	}
	for _, alias := range verified.ProviderUserIDAliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == verified.ProviderUserID {
			continue
		}
		identity, err = s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, alias)
		if err != nil || identity != nil {
			return identity, err
		}
	}
	return nil, nil
}

// canonicalizeTelegramProviderUserID 将历史 OIDC subject 绑定迁移为 Telegram 数字用户 ID。
func (s *Service) canonicalizeTelegramProviderUserID(verified *telegramauthapp.IdentityVerified, identity *externalidentitydomain.Identity) (bool, error) {
	if verified == nil || identity == nil || identity.ProviderUserID == verified.ProviderUserID {
		return false, nil
	}
	occupied, err := s.userOAuthIdentityRepo.GetByProviderUserID(verified.Provider, verified.ProviderUserID)
	if err != nil {
		return false, err
	}
	if occupied != nil && occupied.ID != identity.ID {
		return false, ErrUserOAuthIdentityExists
	}
	identity.ProviderUserID = verified.ProviderUserID
	return true, nil
}

// telegramProviderUserIDMatchesVerified 判断绑定 ID 是否匹配当前 Telegram 身份或其历史别名。
func telegramProviderUserIDMatchesVerified(providerUserID string, verified *telegramauthapp.IdentityVerified) bool {
	providerUserID = strings.TrimSpace(providerUserID)
	if verified == nil || providerUserID == "" {
		return false
	}
	if providerUserID == verified.ProviderUserID {
		return true
	}
	for _, alias := range verified.ProviderUserIDAliases {
		if providerUserID == strings.TrimSpace(alias) {
			return true
		}
	}
	return false
}

// updateTelegramIdentity prevents stale login/profile refreshes from restoring
// an identity deleted or moved by a concurrent unbind/transfer.
func (s *Service) updateTelegramIdentity(identity *externalidentitydomain.Identity) error {
	if s.authUnitOfWork == nil {
		return telegramauthapp.ErrTelegramAuthConfigInvalid
	}
	return s.authUnitOfWork.WithinTransaction(context.Background(), func(tx AuthTransaction) error {
		if _, err := activeTransactionUser(tx, identity.UserID); err != nil {
			return err
		}
		current, err := tx.GetIdentityByUserProvider(identity.UserID, identity.Provider)
		if err != nil {
			return err
		}
		if current == nil || current.ID != identity.ID {
			return ErrUserOAuthNotBound
		}
		return tx.UpdateIdentity(identity)
	})
}

// applyTelegramIdentity merges verified profile fields into identity and returns whether it changed.
// Partial channel input preserves omitted fields; authoritative empty values can still clear them.
func applyTelegramIdentity(verified *telegramauthapp.IdentityVerified, identity *externalidentitydomain.Identity) bool {
	if verified == nil || identity == nil {
		return false
	}
	changed := false
	if identity.Provider == "" {
		identity.Provider = verified.Provider
		changed = true
	}
	if identity.ProviderUserID == "" {
		identity.ProviderUserID = verified.ProviderUserID
		changed = true
	}
	if (!verified.PartialProfile || verified.UsernamePresent) && identity.Username != verified.Username {
		identity.Username = verified.Username
		changed = true
	}
	if (!verified.PartialProfile || verified.AvatarPresent) && identity.AvatarURL != verified.AvatarURL {
		identity.AvatarURL = verified.AvatarURL
		changed = true
	}
	profilePresent := !verified.PartialProfile || verified.UsernamePresent || verified.AvatarPresent || verified.FirstName != "" || verified.LastName != ""
	if profilePresent && (identity.AuthAt == nil || !identity.AuthAt.Equal(verified.AuthAt)) {
		authAt := verified.AuthAt
		identity.AuthAt = &authAt
		changed = true
	}
	return changed
}
