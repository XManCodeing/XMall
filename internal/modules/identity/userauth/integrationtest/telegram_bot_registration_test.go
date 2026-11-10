package integrationtest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/XManCodeing/XMall/internal/constants"
	invitecodedomain "github.com/XManCodeing/XMall/internal/modules/identity/invitecode/domain"
	userdomain "github.com/XManCodeing/XMall/internal/modules/identity/user/domain"
	userauthapp "github.com/XManCodeing/XMall/internal/modules/identity/userauth/application"
)

// TestBotRegistrationPolicy verifies the independent switch and existing-binding behavior.
func TestBotRegistrationPolicy(t *testing.T) {
	svc, settings, db := setupTelegramOAuthTestService(t)
	input := userauthapp.TelegramChannelIdentityInput{ChannelUserID: "89001"}
	if _, _, _, err := svc.ProvisionTelegramBotIdentity(context.Background(), 0, input); err == nil {
		t.Fatal("zero bot ID accepted")
	}
	if _, _, _, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, input); !errors.Is(err, userauthapp.ErrRegistrationDisabled) {
		t.Fatalf("default must be closed: %v", err)
	}
	if _, err := settings.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{constants.SettingFieldRegistrationEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.PatchTelegramBotConfigSection("registration", 0, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	user, identity, created, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, input)
	if err != nil || !created || user == nil || identity == nil {
		t.Fatalf("bot registration must override website switch: %v %v", created, err)
	}
	if _, _, _, err := svc.ProvisionTelegramChannelIdentity(userauthapp.TelegramChannelIdentityInput{ChannelUserID: "89002"}); !errors.Is(err, userauthapp.ErrRegistrationDisabled) {
		t.Fatalf("channel policy changed: %v", err)
	}
	if _, err := settings.PatchTelegramBotConfigSection("registration", 1, []byte(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	resolved, _, created, err := svc.ProvisionTelegramBotIdentity(context.Background(), 2, input)
	if err != nil || created || resolved.ID != user.ID {
		t.Fatalf("existing binding unavailable: %v", err)
	}
	if err := db.Model(&userdomain.User{}).Where("id = ?", user.ID).Update("status", constants.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, input); !errors.Is(err, userauthapp.ErrUserDisabled) {
		t.Fatalf("disabled account accepted: %v", err)
	}
}

// TestBotAndWebsiteRegistrationMatrix checks both switches independently against
// new identities; it never changes existing bindings or external channel policy.
func TestBotAndWebsiteRegistrationMatrix(t *testing.T) {
	for _, websiteEnabled := range []bool{false, true} {
		for _, botEnabled := range []bool{false, true} {
			svc, settings, db := setupTelegramOAuthTestService(t)
			if _, err := settings.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{constants.SettingFieldRegistrationEnabled: websiteEnabled}); err != nil {
				t.Fatal(err)
			}
			payload := []byte(`{"enabled":false}`)
			if botEnabled {
				payload = []byte(`{"enabled":true}`)
			}
			if _, err := settings.PatchTelegramBotConfigSection("registration", 0, payload); err != nil {
				t.Fatal(err)
			}
			_, _, created, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, userauthapp.TelegramChannelIdentityInput{ChannelUserID: "89021"})
			if botEnabled && (err != nil || !created) {
				t.Fatalf("website=%t bot=%t: created=%t error=%v", websiteEnabled, botEnabled, created, err)
			}
			if !botEnabled && (!errors.Is(err, userauthapp.ErrRegistrationDisabled) || created || countUsers(t, db) != 0) {
				t.Fatalf("website=%t bot=%t: bot policy bypassed: %v", websiteEnabled, botEnabled, err)
			}
			_, _, channelCreated, channelErr := svc.ProvisionTelegramChannelIdentity(userauthapp.TelegramChannelIdentityInput{ChannelUserID: "89022"})
			if websiteEnabled && (channelErr != nil || !channelCreated) {
				t.Fatalf("website registration blocked by bot switch: %v", channelErr)
			}
			if !websiteEnabled && !errors.Is(channelErr, userauthapp.ErrRegistrationDisabled) {
				t.Fatalf("channel bypassed website policy: %v", channelErr)
			}
		}
	}
}

// TestBotRegistrationInviteAndConcurrency checks atomic invitation use and retry safety.
func TestBotRegistrationInviteAndConcurrency(t *testing.T) {
	svc, settings, db := setupTelegramOAuthTestService(t)
	if err := db.AutoMigrate(&invitecodedomain.InviteCode{}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Update(constants.SettingKeyRegistrationConfig, map[string]interface{}{
		constants.SettingFieldRegistrationEnabled: false, constants.SettingFieldRegistrationInviteOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.PatchTelegramBotConfigSection("registration", 0, []byte(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	input := userauthapp.TelegramChannelIdentityInput{ChannelUserID: "89003"}
	if _, _, _, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, input); !errors.Is(err, userauthapp.ErrInviteCodeRequired) {
		t.Fatalf("invite policy bypassed: %v", err)
	}
	if countUsers(t, db) != 0 {
		t.Fatal("failed invitation created user")
	}
	createInviteCode(t, db, "BOTONCE", 1)
	input.InviteCode = "BOTONCE"
	var workers sync.WaitGroup
	for index := 0; index < 5; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, _, _, err := svc.ProvisionTelegramBotIdentity(context.Background(), 1, input); err != nil {
				t.Errorf("concurrent provisioning: %v", err)
			}
		}()
	}
	workers.Wait()
	if countUsers(t, db) != 1 {
		t.Fatal("concurrent provisioning created duplicate accounts")
	}
	var code invitecodedomain.InviteCode
	if err := db.Where("code = ?", "BOTONCE").First(&code).Error; err != nil || code.UsedCount != 1 {
		t.Fatalf("invite consumed more than once: %v %+v", err, code)
	}
}
