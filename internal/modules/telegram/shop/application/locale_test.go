package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
)

type localeContent struct {
	fallback string
	locales  []string
}

func (p *localeContent) Content(_ context.Context, a c.Actor) (c.Content, error) {
	p.locales = append(p.locales, a.Locale)
	return c.Content{
		DefaultLocale: p.fallback, WelcomeEnabled: true, Welcome: "Welcome " + a.Locale,
		Menu: []c.MenuItem{{Action: "language", Label: "Language"}},
	}, nil
}

func TestLocaleNewSessionFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, telegram, stored, fallback, want string
	}{
		{"missing language English default", "", "", "en-US", "en-US"},
		{"unsupported language English default", "fr", "", "en-US", "en-US"},
		{"missing language traditional default", "", "", "zh-TW", "zh-TW"},
		{"unsupported language traditional default", "de", "", "zh-TW", "zh-TW"},
		{"missing language missing default", "", "", "", "zh-CN"},
		{"unsupported language invalid default", "ja", "", "fr", "zh-CN"},
		{"Telegram English", "en-GB", "", "zh-TW", "en-US"},
		{"Telegram simplified", "zh-hans", "", "en-US", "zh-CN"},
		{"Telegram traditional", "zh-hant", "", "en-US", "zh-TW"},
		{"Telegram Hong Kong", "zh-HK", "", "en-US", "zh-TW"},
		{"Telegram normalized", " EN-us ", "", "zh-CN", "en-US"},
		{"stored preference", "en", "zh-TW", "zh-CN", "zh-TW"},
		{"unsupported stored preference", "en", "fr", "zh-TW", "en-US"},
		{"runtime unsupported contact", "fr", "fr", "en-US", "en-US"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m, _ := setup()
			provider := &localeContent{fallback: tc.fallback}
			s.d.Content = provider
			if tc.stored != "" {
				m.contacts[11] = rd.Contact{BotID: 1, TelegramUserID: 11, Locale: tc.stored}
			}
			u := update(1, "/start")
			u.Locale = tc.telegram
			out := run(t, s, u)
			assertSavedLocale(t, s, m, tc.want)
			if len(out) != 1 || out[0].Text != "Welcome "+tc.want {
				t.Fatalf("content not resolved with final locale: %+v", out)
			}
			if got := provider.locales[len(provider.locales)-1]; got != tc.want {
				t.Fatalf("content actor locale = %q, want %q", got, tc.want)
			}
		})
	}
}

func assertSavedLocale(t *testing.T, s *Service, m *memoryStore, want string) {
	t.Helper()
	if got := savedState(t, s, m).Locale; got != want {
		t.Fatalf("session locale = %q, want %q", got, want)
	}
	if got := m.contacts[11].Locale; got != want {
		t.Fatalf("contact locale = %q, want %q", got, want)
	}
}

func TestLocaleWithoutContentProvider(t *testing.T) {
	for _, language := range []string{"", "fr"} {
		s, m, _ := setup()
		u := update(1, "/start")
		u.Locale = language
		run(t, s, u)
		assertSavedLocale(t, s, m, "zh-CN")
	}
}

func TestLocalePreservesValidSession(t *testing.T) {
	s, m, _ := setup()
	u := update(1, "/start")
	u.Locale = "zh-Hant"
	run(t, s, u)
	contact := m.contacts[11]
	contact.Locale = "en-US"
	m.contacts[11] = contact
	s.d.Content = &localeContent{fallback: "en-US"}
	u = update(2, "/start")
	u.Locale = "zh-CN"
	run(t, s, u)
	assertSavedLocale(t, s, m, "zh-TW")
}

func TestLocaleSelectionPersistsImmediatelyAndReplays(t *testing.T) {
	for _, tc := range []struct{ label, want string }{{"English", "en-US"}, {"繁體中文", "zh-TW"}} {
		t.Run(tc.want, func(t *testing.T) {
			s, m, _ := setup()
			provider := &localeContent{fallback: "zh-CN"}
			s.d.Content = provider
			home := run(t, s, update(1, "/start"))
			menu := click(t, s, 2, home, "Language")
			u := update(3, "")
			u.Locale = "zh-CN"
			u.CallbackData = button(t, menu, tc.label)
			out := run(t, s, u)
			assertSavedLocale(t, s, m, tc.want)
			if out[0].Text != "Welcome "+tc.want {
				t.Fatalf("selection content = %+v", out)
			}
			before := m.sessions[sessionKey(1, 11, 11)].Version
			calls := len(provider.locales)
			if replay := run(t, s, u); !reflect.DeepEqual(out, replay) {
				t.Fatalf("replay changed reply: %+v", replay)
			}
			if m.sessions[sessionKey(1, 11, 11)].Version != before || len(provider.locales) != calls {
				t.Fatal("replay rerendered or saved session")
			}
			assertSavedLocale(t, s, m, tc.want)
			if out := run(t, s, update(2, "/start")); len(out) != 0 {
				t.Fatal("older update was not ignored")
			}
			assertSavedLocale(t, s, m, tc.want)
			row := m.sessions[sessionKey(1, 11, 11)]
			row.ExpiresAt = s.now().Add(-time.Minute)
			m.sessions[sessionKey(1, 11, 11)] = row
			u = update(4, "/start")
			u.Locale = "zh-CN"
			run(t, s, u)
			assertSavedLocale(t, s, m, tc.want)
		})
	}
}

type localeFailStore struct {
	*memoryStore
	failLocale string
}

func (m *localeFailStore) SaveContact(ctx context.Context, contact *rd.Contact) error {
	if contact.Locale == m.failLocale {
		m.failLocale = ""
		return errors.New("contact save failed")
	}
	return m.memoryStore.SaveContact(ctx, contact)
}

func TestLocaleSelectionReplayRepairsFailedContactSave(t *testing.T) {
	s, m, _ := setup()
	store := &localeFailStore{memoryStore: m, failLocale: "en-US"}
	s.d.Store = store
	home := run(t, s, update(1, "/start"))
	menu := click(t, s, 2, home, text("zh-CN", "language"))
	u := update(3, "")
	u.CallbackData = button(t, menu, "English")
	if _, err := s.Handle(context.Background(), 1, u); err == nil {
		t.Fatal("contact save failure was hidden")
	}
	st := savedState(t, s, m)
	if st.Locale != "en-US" || m.contacts[11].Locale != "zh-CN" {
		t.Fatal("unexpected partial save state")
	}
	if replay := run(t, s, u); !reflect.DeepEqual(st.Reply, replay) {
		t.Fatal("committed reply was not replayed")
	}
	assertSavedLocale(t, s, m, "en-US")
}

func TestLocaleSelectionSessionConflictDoesNotPersistPreference(t *testing.T) {
	s, m, _ := setup()
	home := run(t, s, update(1, "/start"))
	menu := click(t, s, 2, home, text("zh-CN", "language"))
	u := update(3, "")
	u.CallbackData = button(t, menu, "English")
	m.fail = true
	if _, err := s.Handle(context.Background(), 1, u); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("expected session conflict, got %v", err)
	}
	assertSavedLocale(t, s, m, "zh-CN")
	run(t, s, u)
	assertSavedLocale(t, s, m, "en-US")
}
