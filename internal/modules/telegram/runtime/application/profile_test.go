package application

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
)

type profileStore struct {
	c.Store
	row     d.Instance
	saved   map[string]interface{}
	version uint64
}

func (s *profileStore) Get(context.Context, uint) (*d.Instance, error) {
	row := s.row
	return &row, nil
}
func (s *profileStore) SaveProfile(_ context.Context, _ uint, version uint64, values map[string]interface{}) error {
	current := s.row.ProfileVersion
	if current == 0 {
		current = 1
	}
	if current != version {
		return c.ErrConflict
	}
	s.saved = values
	s.row.ProfileVersion = version + 1
	s.version = s.row.ProfileVersion
	if name, ok := values["profile_name"].(string); ok {
		s.row.ProfileName = name
	}
	if desc, ok := values["profile_description"].(string); ok {
		s.row.ProfileDescription = desc
	}
	if cover, ok := values["profile_cover_url"].(string); ok {
		s.row.ProfileCoverURL = cover
	}
	if syncErr, ok := values["profile_sync_error"].(string); ok {
		s.row.ProfileSyncError = syncErr
	}
	return nil
}

type profileAPI struct {
	c.API
	last    c.BotProfile
	fail    bool
	tokenOK bool
}

func (a *profileAPI) Identity(context.Context, string) (c.BotIdentity, error) {
	return c.BotIdentity{ID: 1}, nil
}
func (a *profileAPI) ApplyBotProfile(_ context.Context, _ string, profile c.BotProfile) error {
	a.last = profile
	if a.fail {
		return errors.New("upstream")
	}
	return nil
}

func TestUpdateProfileSyncsSelectedInstanceOnly(t *testing.T) {
	cipher, err := NewManager(nil, nil, "secret").Seal("123:token")
	if err != nil {
		t.Fatal(err)
	}
	store := &profileStore{row: d.Instance{ID: 9, Username: "shopbot", TokenCipher: cipher, ProfileVersion: 1}}
	api := &profileAPI{}
	manager := NewManager(store, api, "secret")
	view, err := manager.UpdateProfile(context.Background(), 9, 1, BotProfileInput{
		DisplayName: "Card Shop",
		Description: map[string]string{"zh-CN": "买卡", "en-US": "Buy cards"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.DisplayName != "Card Shop" || view.ProfileVersion != 2 || api.last.Name != "Card Shop" {
		t.Fatalf("view=%+v api=%+v", view, api.last)
	}
	if api.last.Descriptions["en-US"] != "Buy cards" || api.last.Descriptions["zh-CN"] != "买卡" {
		t.Fatalf("descriptions: %#v", api.last.Descriptions)
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(store.row.ProfileDescription), &stored); err != nil || stored["en-US"] != "Buy cards" {
		t.Fatal(store.row.ProfileDescription)
	}
}

func TestEncodeProfilePhotoUpscalesAndSquares(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 40))
	data, err := encodeProfilePhoto(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || cfg.Width != profilePhotoSize || cfg.Height != profilePhotoSize {
		t.Fatalf("got %s %dx%d err=%v", format, cfg.Width, cfg.Height, err)
	}
}

func TestUpdateProfileConflictAndSyncFailure(t *testing.T) {
	cipher, err := NewManager(nil, nil, "secret").Seal("123:token")
	if err != nil {
		t.Fatal(err)
	}
	store := &profileStore{row: d.Instance{ID: 1, TokenCipher: cipher, ProfileVersion: 3}}
	manager := NewManager(store, &profileAPI{}, "secret")
	if _, err := manager.UpdateProfile(context.Background(), 1, 1, BotProfileInput{}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	store.row.ProfileVersion = 1
	api := &profileAPI{fail: true}
	manager = NewManager(store, api, "secret")
	view, err := manager.UpdateProfile(context.Background(), 1, 1, BotProfileInput{DisplayName: "X"})
	if err == nil || !strings.Contains(err.Error(), "sync failed") || view.ProfileSyncError == "" {
		t.Fatalf("sync failure: %+v %v", view, err)
	}
}

func mp4Box(kind string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], kind)
	return append(out, body...)
}

func testMP4(w, h uint32) []byte {
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:], w<<16)
	binary.BigEndian.PutUint32(tkhd[80:], h<<16)
	audio := mp4Box("trak", mp4Box("tkhd", make([]byte, 84)))
	video := mp4Box("trak", mp4Box("tkhd", tkhd))
	return append(mp4Box("ftyp", []byte("isom0000")), mp4Box("moov", mp4Box("mvhd", make([]byte, 100)), audio, video)...)
}

func TestValidateProfileVideo(t *testing.T) {
	if err := validateProfileVideo(testMP4(800, 800)); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{testMP4(800, 600), testMP4(1280, 1280), mp4Box("ftyp", []byte("isom0000"))} {
		if err := validateProfileVideo(data); !errors.Is(err, errProfileVideo) {
			t.Fatalf("expected rejection, got %v", err)
		}
	}
}

func TestUpdateProfileUploadsAnimatedAvatar(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "telegram"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "telegram", "a.mp4"), testMP4(800, 800), 0o644); err != nil {
		t.Fatal(err)
	}
	cipher, err := NewManager(nil, nil, "secret").Seal("123:token")
	if err != nil {
		t.Fatal(err)
	}
	store := &profileStore{row: d.Instance{ID: 2, TokenCipher: cipher, ProfileVersion: 1}}
	api := &profileAPI{}
	manager := NewManagerWithUploadRoot(store, api, "secret", root)
	if _, err := manager.UpdateProfile(context.Background(), 2, 1, BotProfileInput{CoverURL: "/uploads/telegram/a.mp4"}); err != nil {
		t.Fatal(err)
	}
	if len(api.last.CoverMP4) == 0 || len(api.last.CoverJPEG) != 0 {
		t.Fatalf("expected animated upload: %+v", api.last)
	}
}
