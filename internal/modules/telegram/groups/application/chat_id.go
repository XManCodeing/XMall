package application

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
)

// resolveChatID accepts an explicit negative Bot API ID unchanged, or resolves a
// positive user-supplied number through read-only Telegram probes. It returns a
// canonical group ID and its successful probe, if performed, for same-request reuse.
func (s *Service) resolveChatID(ctx context.Context, bot uint, input string) (int64, *c.ProbeResult, error) {
	value := strings.TrimSpace(input)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, nil, c.ErrInvalid
	}
	if parsed < 0 {
		return parsed, nil, nil
	}
	// Telegram marks channel/supergroup IDs by -(1000000000000 + raw ID),
	// not by concatenating "-100" (which is wrong for non-ten-digit raw IDs).
	const channelOffset int64 = 1000000000000
	candidates := []int64{-parsed}
	if parsed < channelOffset {
		candidates = append(candidates, -(channelOffset + parsed))
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var matches []int64
	var matched *c.ProbeResult
	for _, candidate := range candidates {
		result := s.Probe.Check(probeCtx, bot, candidate)
		if result.ErrorClass == "resource_inaccessible" {
			continue
		}
		if result.ErrorClass != "" {
			return 0, nil, c.ErrChatUnresolved
		}
		kind := result.Observation.ChatType
		// Never register private chats or broadcast channels as group targets.
		if (candidate > -channelOffset && kind == "group") || (candidate < -channelOffset && kind == "supergroup") {
			matches = append(matches, candidate)
			matched = &result
		}
	}
	if len(matches) > 1 {
		return 0, nil, c.ErrChatAmbiguous
	}
	if len(matches) == 0 {
		return 0, nil, c.ErrChatUnresolved
	}
	return matches[0], matched, nil
}

// creationProbe verifies bot's canonical chat and returns its exact Telegram title.
// resolved is reusable only within this request for the same bot/chat. Titles that
// cannot fit the existing 120-character name column are rejected, never truncated.
func (s *Service) creationProbe(ctx context.Context, bot uint, chat int64, resolved *c.ProbeResult) (*c.ProbeResult, error) {
	if resolved == nil {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		result := s.Probe.Check(probeCtx, bot, chat)
		resolved = &result
	}
	observation := resolved.Observation
	if resolved.ErrorClass != "" || (observation.ChatType != "group" && observation.ChatType != "supergroup") || strings.TrimSpace(observation.Title) == "" {
		return nil, c.ErrChatUnresolved
	}
	if !utf8.ValidString(observation.Title) || utf8.RuneCountInString(observation.Title) > 120 {
		return nil, c.ErrInvalid
	}
	return resolved, nil
}
