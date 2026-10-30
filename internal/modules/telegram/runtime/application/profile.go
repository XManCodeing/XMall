package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/productmedia"
)

// BotProfileView is the admin DTO for one instance's Telegram identity.
type BotProfileView struct {
	InstanceID       uint              `json:"instance_id"`
	Username         string            `json:"username"`
	DisplayName      string            `json:"display_name"`
	Description      map[string]string `json:"description"`
	CoverURL         string            `json:"cover_url"`
	ProfileVersion   uint64            `json:"profile_version"`
	ProfileSyncError string            `json:"profile_sync_error,omitempty"`
}

// BotProfileInput is the admin write payload.
type BotProfileInput struct {
	DisplayName string            `json:"display_name"`
	Description map[string]string `json:"description"`
	CoverURL    string            `json:"cover_url"`
}

func (m *Manager) GetProfile(ctx context.Context, id uint) (BotProfileView, error) {
	instance, err := m.Store.Get(ctx, id)
	if err != nil {
		return BotProfileView{}, err
	}
	return profileView(instance), nil
}

func profileView(instance *d.Instance) BotProfileView {
	desc := map[string]string{"zh-CN": "", "zh-TW": "", "en-US": ""}
	if strings.TrimSpace(instance.ProfileDescription) != "" {
		_ = json.Unmarshal([]byte(instance.ProfileDescription), &desc)
	}
	for _, key := range []string{"zh-CN", "zh-TW", "en-US"} {
		if _, ok := desc[key]; !ok {
			desc[key] = ""
		}
	}
	version := instance.ProfileVersion
	if version == 0 {
		version = 1
	}
	return BotProfileView{
		InstanceID:       instance.ID,
		Username:         instance.Username,
		DisplayName:      instance.ProfileName,
		Description:      desc,
		CoverURL:         instance.ProfileCoverURL,
		ProfileVersion:   version,
		ProfileSyncError: instance.ProfileSyncError,
	}
}

func (v BotProfileView) descriptionJSON() string {
	encoded, _ := json.Marshal(v.Description)
	return string(encoded)
}

// UpdateProfile persists and syncs Telegram Bot identity for exactly one instance.
func (m *Manager) UpdateProfile(ctx context.Context, id uint, expected uint64, input BotProfileInput) (BotProfileView, error) {
	instance, err := m.Store.Get(ctx, id)
	if err != nil {
		return BotProfileView{}, err
	}
	currentVersion := instance.ProfileVersion
	if currentVersion == 0 {
		currentVersion = 1
	}
	if currentVersion != expected {
		return BotProfileView{}, c.ErrConflict
	}
	name := strings.TrimSpace(input.DisplayName)
	if utf8.RuneCountInString(name) > 64 {
		return BotProfileView{}, errors.New("display name exceeds 64 characters")
	}
	desc := map[string]string{"zh-CN": "", "zh-TW": "", "en-US": ""}
	for _, key := range []string{"zh-CN", "zh-TW", "en-US"} {
		value := strings.TrimSpace(input.Description[key])
		if utf8.RuneCountInString(value) > 512 {
			return BotProfileView{}, errors.New("description exceeds 512 characters")
		}
		desc[key] = value
	}
	cover := strings.TrimSpace(input.CoverURL)
	if cover != "" && !strings.HasPrefix(cover, "/uploads/") {
		return BotProfileView{}, errors.New("cover must be a local upload path")
	}
	encoded, err := json.Marshal(desc)
	if err != nil {
		return BotProfileView{}, err
	}

	token, err := m.Token(instance)
	if err != nil {
		return BotProfileView{}, errors.New("bot credential unavailable")
	}
	profileAPI, ok := m.API.(c.ProfileAPI)
	if !ok {
		return BotProfileView{}, errors.New("telegram profile sync unavailable")
	}

	// A previous partial failure leaves Telegram state unknown, so resync everything.
	retry := instance.ProfileSyncError != ""
	tg := c.BotProfile{
		Name:            name,
		SyncName:        retry || name != instance.ProfileName,
		Descriptions:    desc,
		SyncDescription: retry || string(encoded) != profileView(instance).descriptionJSON(),
	}
	prevCover := strings.TrimSpace(instance.ProfileCoverURL)
	if cover == "" && prevCover != "" {
		tg.ClearCover = true
	} else if cover != "" && (cover != prevCover || retry) {
		media, readErr := m.readProfileMedia(cover)
		if readErr != nil {
			return BotProfileView{}, readErr
		}
		if media.animated {
			tg.CoverMP4 = media.data
		} else {
			tg.CoverJPEG = media.data
		}
	}

	fields := map[string]interface{}{
		"profile_name": name, "profile_description": string(encoded), "profile_cover_url": cover, "profile_sync_error": "",
	}
	if err := profileAPI.ApplyBotProfile(ctx, token, tg); err != nil {
		fields["profile_sync_error"] = "telegram profile sync failed"
		if saveErr := m.Store.SaveProfile(ctx, id, expected, fields); saveErr != nil {
			return BotProfileView{}, errors.New("telegram profile sync failed")
		}
		view, getErr := m.GetProfile(ctx, id)
		if getErr != nil {
			return BotProfileView{}, errors.New("telegram profile sync failed")
		}
		return view, errors.New("telegram profile sync failed")
	}
	if err := m.Store.SaveProfile(ctx, id, expected, fields); err != nil {
		return BotProfileView{}, errors.New("telegram profile persist failed")
	}
	return m.GetProfile(ctx, id)
}

const maxProfileMediaBytes = 10 << 20

type profileMedia struct {
	data     []byte
	animated bool
}

func (m *Manager) readProfileMedia(ref string) (profileMedia, error) {
	if strings.TrimSpace(m.uploadRoot) == "" {
		return profileMedia{}, errors.New("upload root unavailable")
	}
	data, err := productmedia.ReadUploadFile(m.uploadRoot, ref, maxProfileMediaBytes)
	if err != nil {
		return profileMedia{}, errors.New("cover image unavailable")
	}
	if isMP4(data) {
		if err := validateProfileVideo(data); err != nil {
			return profileMedia{}, err
		}
		return profileMedia{data: data, animated: true}, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 25000000 {
		return profileMedia{}, errors.New("cover image unavailable")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return profileMedia{}, errors.New("cover image unavailable")
	}
	encoded, err := encodeProfilePhoto(img)
	if err != nil {
		return profileMedia{}, err
	}
	return profileMedia{data: encoded}, nil
}

// Telegram rejects small or non-square profile photos (PHOTO_INVALID_DIMENSIONS),
// so every source is center-cropped to a square and scaled to a fixed size.
const profilePhotoSize = 640

func encodeProfilePhoto(src image.Image) ([]byte, error) {
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	if side <= 0 {
		return nil, errors.New("cover image unavailable")
	}
	origin := image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2)
	crop := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(side, side))}
	dst := image.NewRGBA(image.Rect(0, 0, profilePhotoSize, profilePhotoSize))
	// JPEG has no alpha; flatten transparent pixels onto white instead of black.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 90}); err != nil {
		return nil, errors.New("cover image unavailable")
	}
	return out.Bytes(), nil
}
