package contract

import (
	"context"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
)

// RecoveryInput identifies both physical routes explicitly. ExpectedVersion and
// ExpectedRouteVersion are the persisted target CAS snapshot for this phase.
// TaskID is a durable orchestration identity, never a user-provided lease token.
type RecoveryInput struct {
	TargetID             uint
	TaskID               uint
	ExpectedVersion      uint64
	ExpectedRouteVersion uint64
	OldBotID             uint
	OldChatID            int64
	NewBotID             uint
	NewChatID            int64
}

// RecoveryRelease requires the new route and the exact fresh observation seen by
// the orchestrator. Generation and BotTelegramID come from its trusted runtime.
type RecoveryRelease struct {
	TargetID               uint
	TaskID                 uint
	ExpectedVersion        uint64
	ExpectedRouteVersion   uint64
	ExpectedObservationSeq uint64
	BotID                  uint
	ChatID                 int64
	Generation             uint64
	BotTelegramID          int64
}

// RecoveryResult never migrates module configuration. CrossBot tells the caller
// it must explicitly reconcile the old/new Bot module partitions. Ordinary
// module writes remain blocked until release; no configuration is auto-transferred.
type RecoveryResult struct {
	Target                   d.Target
	CrossBot                 bool
	ModuleConfigurationMoved bool
}

// RecoveryStore is deliberately separate from Store so read-only fakes and
// existing consumers do not gain route-recovery authority. Methods perform only
// local transactions; platform checks and task orchestration belong to callers.
type RecoveryStore interface {
	FreezeTarget(context.Context, RecoveryInput) (*d.Target, error)
	ReplaceRoute(context.Context, RecoveryInput) (*RecoveryResult, error)
	ReleaseRecovery(context.Context, RecoveryRelease) (*d.Target, error)
}
