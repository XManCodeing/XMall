package contract

import (
	"context"
	"fmt"
	g "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	r "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
	"strings"
	"time"
)

// SupportMembers is implemented by the groups application service. Every call
// verifies the current live actor membership; never invoke it inside a DB tx.
type SupportMembers interface {
	ResolveSupportRoute(context.Context, uint) (g.SupportRoute, error)
	AuthorizeSupportMember(context.Context, uint, int64, int64, uint64) error
}

// DownstreamCommand reports whether observed text/caption is a bot command.
// Such messages are control-plane inputs, never customer-facing replies.
func DownstreamCommand(update r.Update) bool {
	if strings.HasPrefix(strings.TrimSpace(update.Text), "/") || strings.HasPrefix(strings.TrimSpace(update.Caption), "/") {
		return true
	}
	for _, entities := range [][]r.MessageEntity{update.Entities, update.CaptionEntities} {
		for _, entity := range entities {
			if entity.Type == "bot_command" {
				return true
			}
		}
	}
	return false
}

// Downstreams persists only references already resolved against native mappings.
// Application must recheck live membership before calling AcceptDownstream and
// again before returning authorization for an external send.
type Downstreams interface {
	AcceptDownstream(context.Context, uint, r.Update, bool, time.Time) (*d.Downstream, error)
	DownstreamRequest(context.Context, uint, uint, time.Time) (*r.DeliveryRequest, error)
	CheckDownstream(context.Context, uint, r.Message, time.Time) (*d.Downstream, error)
	ScanDownstream(context.Context, uint, uint, int) ([]d.Downstream, error)
	ReconcileDownstream(context.Context, uint, uint, uint64, time.Time) (string, error)
}

// DownstreamResolutions resolves unknown business state only from an existing
// runtime receipt. "delivered" requires complete sent IDs; "failed" requires a
// terminal failed row with NO observed IDs and reopens only that same runtime key.
// Caller must authorize administrators independently of group member privileges.
type DownstreamResolutions interface {
	ResolveDownstream(context.Context, uint, uint, uint64, Resolution, time.Time) error
}

// DownstreamBusinessRef is the stable runtime key and encrypted business ref.
func DownstreamBusinessRef(id uint) string { return fmt.Sprintf("support:reply:%d", id) }
