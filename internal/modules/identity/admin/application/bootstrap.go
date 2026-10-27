package adminapplication

import (
	"strings"

	"github.com/dujiao-next/internal/logger"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"

	"golang.org/x/crypto/bcrypt"
)

const defaultBootstrapUsername = "admin"

// BootstrapStore counts all historical administrators, including deleted ones.
// Removing the last account must not cause automatic bootstrap on restart.
type BootstrapStore interface {
	CountAll() (int64, error)
	Create(*admindomain.Admin) error
}

// InitDefaultAdmin creates the first administrator only. Existing accounts,
// including a demoted bootstrap administrator, are never modified.
func InitDefaultAdmin(store BootstrapStore, username, password string) error {
	bootstrapUsername := normalizeBootstrapUsername(username)
	count, err := store.CountAll()
	if err != nil {
		return err
	}
	if count > 0 || strings.TrimSpace(password) == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := &admindomain.Admin{Username: bootstrapUsername, PasswordHash: string(hash), IsSuper: true}
	if err := store.Create(admin); err != nil {
		return err
	}
	logger.Warnw("default_admin_created", "username", bootstrapUsername, "password_hidden", true)
	return nil
}

func normalizeBootstrapUsername(username string) string {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return defaultBootstrapUsername
	}
	return trimmed
}
