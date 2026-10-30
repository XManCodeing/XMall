package productmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

const maxProductPhoto = 10 * 1024 * 1024

type Cache interface {
	CachedMedia(context.Context, uint, string, string) (string, error)
	CacheMedia(context.Context, uint, string, string, string) error
}
type API struct {
	rt.API
	root  string
	cache Cache
}

func New(api rt.API, root string, cache Cache) *API { return &API{API: api, root: root, cache: cache} }

// ApplyBotProfile forwards to the underlying adapter. Embedding rt.API alone does
// not expose ProfileAPI methods through the wrapper type assertion.
func (a *API) ApplyBotProfile(ctx context.Context, token string, profile rt.BotProfile) error {
	profileAPI, ok := a.API.(rt.ProfileAPI)
	if !ok {
		return errors.New("telegram profile sync unavailable")
	}
	return profileAPI.ApplyBotProfile(ctx, token, profile)
}
func photoHashes(ref string, data []byte) (string, string) {
	p, c := sha256.Sum256([]byte(ref)), sha256.Sum256(data)
	return hex.EncodeToString(p[:]), hex.EncodeToString(c[:])
}

// ReadUploadPhoto opens a local /uploads image with the same traversal and
// format guards used by product panels.
func ReadUploadPhoto(root, ref string) ([]byte, string, error) {
	return readProductPhoto(root, ref)
}

// ReadUploadFile opens a regular file under /uploads without format checks.
func ReadUploadFile(root, ref string, maxBytes int64) ([]byte, error) {
	return readUploadFile(root, ref, maxBytes)
}

func readProductPhoto(root, ref string) ([]byte, string, error) {
	data, err := readUploadFile(root, ref, maxProductPhoto)
	if err != nil {
		return nil, "", err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width+cfg.Height > 10000 || int64(cfg.Width)*int64(cfg.Height) > 25000000 || cfg.Width > 20*cfg.Height || cfg.Height > 20*cfg.Width {
		return nil, "", errors.New("unsupported product image")
	}
	extension := ".png"
	if format == "jpeg" {
		extension = ".jpg"
	}
	return data, "product" + extension, nil
}

func readUploadFile(root, ref string, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(ref)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/uploads/") {
		return nil, errors.New("invalid product image reference")
	}
	name := strings.TrimPrefix(u.Path, "/uploads/")
	if name == "" || strings.Contains(name, "\\") || path.Clean(name) != name {
		return nil, errors.New("invalid product image path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.HasPrefix(part, ".env") {
			return nil, errors.New("invalid product image path")
		}
	}
	// Root prevents symlink escapes even when paths change during opening.
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.New("product image unavailable")
	}
	defer dir.Close()
	// Uploads are regular files; reject symlinks (including in-root links) and
	// special files before opening so a FIFO cannot block the delivery worker.
	parts := strings.Split(name, "/")
	for i := range parts {
		info, statErr := dir.Lstat(strings.Join(parts[:i+1], "/"))
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || (i == len(parts)-1 && !info.Mode().IsRegular()) || (i < len(parts)-1 && !info.IsDir()) {
			return nil, errors.New("invalid product image path")
		}
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, errors.New("product image unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("invalid product image size")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, errors.New("invalid product image size")
	}
	return data, nil
}

type mediaTransition struct{}

func (mediaTransition) Error() string     { return "media panel requires text replacement" }
func (mediaTransition) Kind() string      { return "media_transition" }
func (mediaTransition) StatusCode() int   { return 400 }
func (mediaTransition) RetrySeconds() int { return 0 }
func (mediaTransition) Uncertain() bool   { return false }

func applyMedia(ctx context.Context, api rt.ApplyingAPI, token string, m rt.Message) (rt.DeliveryResult, error) {
	if m.Operation == "edit" && m.TargetKind == "photo" && len(m.Photo) == 0 && m.PhotoURL == "" && m.FileID == "" {
		return rt.DeliveryResult{}, mediaTransition{}
	}
	result, err := api.Apply(ctx, token, m)
	if err == nil && result.MessageKind == "" {
		result.MessageKind = "text"
		if len(m.Photo) > 0 || m.PhotoURL != "" || m.FileID != "" {
			result.MessageKind = "photo"
		}
		if m.ForceDocument {
			result.MessageKind = "document"
		}
	}
	return result, err
}

// Apply never downloads external images or retries an uncertain send.
func (a *API) Apply(ctx context.Context, token string, message rt.Message) (rt.DeliveryResult, error) {
	api, ok := a.API.(rt.ApplyingAPI)
	if !ok {
		return rt.DeliveryResult{}, errors.New("telegram editing unavailable")
	}
	if message.PhotoURL == "" || message.Operation == "delete" || message.Operation == "markup" {
		return applyMedia(ctx, api, token, message)
	}
	ref := message.PhotoURL
	local := strings.HasPrefix(ref, "/uploads/")
	var pathHash, contentHash string
	var localData []byte
	var localName string
	bot, _ := strconv.ParseUint(strings.SplitN(token, ":", 2)[0], 10, 64)
	if local {
		data, name, err := readProductPhoto(a.root, ref)
		if err != nil {
			slog.Warn("telegram product image omitted", "reason", "invalid local media")
			message.PhotoURL = ""
			return applyMedia(ctx, api, token, message)
		}
		message.PhotoURL = ""
		message.Photo = data
		message.MediaName = name
		localData, localName = data, name
		pathHash, contentHash = photoHashes(ref, data)
		if a.cache != nil && bot > 0 {
			cached, err := a.cache.CachedMedia(ctx, uint(bot), pathHash, contentHash)
			if err == nil && cached != "" {
				message.FileID = cached
				message.Photo = nil
			}
		}
	} else {
		u, err := url.Parse(ref)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			message.PhotoURL = ""
			return applyMedia(ctx, api, token, message)
		}
	}
	result, err := applyMedia(ctx, api, token, message)
	var classified interface{ Kind() string }
	var apiError rt.APIError
	if err != nil && errors.As(err, &classified) && classified.Kind() == "invalid_media" && errors.As(err, &apiError) && !apiError.Uncertain() {
		if local && message.FileID != "" {
			message.FileID = ""
			message.Photo, message.MediaName = localData, localName
			if len(message.Photo) > 0 {
				result, err = applyMedia(ctx, api, token, message)
			}
		}
		if err != nil && errors.As(err, &classified) && classified.Kind() == "invalid_media" && errors.As(err, &apiError) && !apiError.Uncertain() {
			slog.Warn("telegram product image omitted", "reason", "media rejected")
			message.Photo = nil
			message.FileID = ""
			message.PhotoURL = ""
			result, err = applyMedia(ctx, api, token, message)
		}
	}
	if err == nil && local && a.cache != nil && bot > 0 && result.FileID != "" {
		_ = a.cache.CacheMedia(ctx, uint(bot), pathHash, contentHash, result.FileID)
	}
	return result, err
}
