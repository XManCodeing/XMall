package identitymigration

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
)

// request contains validated native values; versions remain uint64 without float conversion.
type request struct {
	oldID, newID int64
	targets      []uint
	versions     map[uint]uint64
}

// decimal trims and parses decimal-only raw within bits, allowing zero only when requested.
// Leading zeroes normalize; signs, exponent notation, fractions and overlong input fail.
func decimal(raw string, bits int, zero bool) (uint64, error) {
	if len(raw) > 64 {
		return 0, c.ErrInvalid
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, c.ErrInvalid
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, c.ErrInvalid
		}
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil || (!zero && parsed == 0) {
		return 0, c.ErrInvalid
	}
	return parsed, nil
}

// normalize bounds input before deduplication and requires complete expected versions if present.
// It returns independent sorted target IDs and detects contradictory normalized version keys.
func normalize(input c.IdentityMigrationInput, at time.Time) (request, error) {
	out := request{versions: make(map[uint]uint64)}
	if input.AccountID == 0 || uint64(input.AccountID) > uint64(1<<63-1) || len(input.TargetIDs) == 0 || len(input.TargetIDs) > 100 || len(input.ExpectedVersions) > 100 || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 {
		return out, c.ErrInvalid
	}
	oldID, err := decimal(input.OldTelegramID, 63, false)
	if err != nil {
		return out, fmt.Errorf("old Telegram ID: %w", err)
	}
	newID, err := decimal(input.NewTelegramID, 63, false)
	if err != nil || newID == oldID {
		return out, fmt.Errorf("new Telegram ID: %w", c.ErrInvalid)
	}
	out.oldID, out.newID = int64(oldID), int64(newID)
	unique := make(map[uint]bool)
	for _, raw := range input.TargetIDs {
		id, err := decimal(raw, 63, false)
		if err != nil || uint64(uint(id)) != id {
			return out, c.ErrInvalid
		}
		if !unique[uint(id)] {
			out.targets = append(out.targets, uint(id))
			unique[uint(id)] = true
		}
	}
	sort.Slice(out.targets, func(left, right int) bool { return out.targets[left] < out.targets[right] })
	for rawID, rawVersion := range input.ExpectedVersions {
		id, err := decimal(rawID, 63, false)
		if err != nil || uint64(uint(id)) != id || !unique[uint(id)] {
			return out, c.ErrInvalid
		}
		version, err := decimal(rawVersion, 64, true)
		if err != nil {
			return out, c.ErrInvalid
		}
		previous, exists := out.versions[uint(id)]
		if exists && previous != version {
			return out, c.ErrInvalid
		}
		out.versions[uint(id)] = version
	}
	if len(input.ExpectedVersions) > 0 && len(out.versions) != len(out.targets) {
		return out, c.ErrInvalid
	}
	return out, nil
}
