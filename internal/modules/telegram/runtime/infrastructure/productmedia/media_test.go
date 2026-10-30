package productmedia

import (
	"bytes"
	"context"
	"errors"
	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func testPhoto(t *testing.T, root string) string {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "product.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return "/uploads/product.png"
}
func TestProductImageConfinedToUploadRoot(t *testing.T) {
	root := t.TempDir()
	good := testPhoto(t, root)
	if b, name, err := readProductPhoto(root, good); err != nil || len(b) == 0 || name != "product.png" {
		t.Fatalf("valid image: %q %v", name, err)
	}
	outside := t.TempDir()
	testPhoto(t, outside)
	if err := os.Symlink(filepath.Join(outside, "product.png"), filepath.Join(root, "escape.png")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"/uploads/../product.png", "/uploads/%2e%2e/product.png", "/uploads/escape.png", "/etc/passwd", "/uploads/product.png?x=1", "https://example.com/a.png", "/uploads/.env", "/uploads/a\\b.png"} {
		if _, _, err := readProductPhoto(root, ref); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bad.png"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProductPhoto(root, "/uploads/bad.png"); err == nil {
		t.Fatal("accepted corrupt image")
	}
}

func TestProductImageLimitsAndContentInvalidation(t *testing.T) {
	root := t.TempDir()
	ref := testPhoto(t, root)
	first, _, err := readProductPhoto(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	p, h := photoHashes(ref, first)
	var data bytes.Buffer
	if err = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 9, 9))); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "product.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	second, _, err := readProductPhoto(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	p2, h2 := photoHashes(ref, second)
	if p2 != p || h2 == h {
		t.Fatal("content update not invalidating cache")
	}
	data.Reset()
	if err = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 10001, 1))); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "wide.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readProductPhoto(root, "/uploads/wide.png"); err == nil {
		t.Fatal("accepted excessive image dimensions")
	}
	f, err := os.Create(filepath.Join(root, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxProductPhoto + 1); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readProductPhoto(root, "/uploads/large.png"); err == nil {
		t.Fatal("accepted oversized upload")
	}
}

type mediaAPIFake struct {
	rt.API
	calls []rt.Message
	err   error
}

func (f *mediaAPIFake) Apply(_ context.Context, _ string, m rt.Message) (rt.DeliveryResult, error) {
	f.calls = append(f.calls, m)
	return rt.DeliveryResult{MessageID: 7, FileID: "file-one"}, f.err
}

type mediaFailure struct {
	kind    string
	unknown bool
}

func (e mediaFailure) Error() string     { return "media failure" }
func (e mediaFailure) Kind() string      { return e.kind }
func (e mediaFailure) StatusCode() int   { return 400 }
func (e mediaFailure) RetrySeconds() int { return 0 }
func (e mediaFailure) Uncertain() bool   { return e.unknown }

type rejectingMediaAPI struct {
	rt.API
	calls   []rt.Message
	unknown bool
}

func (f *rejectingMediaAPI) Apply(_ context.Context, _ string, m rt.Message) (rt.DeliveryResult, error) {
	f.calls = append(f.calls, m)
	if m.FileID != "" || m.PhotoURL != "" || len(m.Photo) > 0 {
		return rt.DeliveryResult{}, mediaFailure{kind: "invalid_media", unknown: f.unknown}
	}
	return rt.DeliveryResult{MessageID: 9}, nil
}
func TestFailedPhotoEditRequestsReplacementBeforeTextSend(t *testing.T) {
	api := &rejectingMediaAPI{}
	a := New(api, t.TempDir(), nil)
	_, err := a.Apply(context.Background(), "123:test", rt.Message{Purpose: "panel", Operation: "edit", TargetKind: "photo", TargetMessageID: 7, Text: "keep buying", PhotoURL: "https://example.invalid/photo.png"})
	var classified interface{ Kind() string }
	if !errors.As(err, &classified) || classified.Kind() != "media_transition" || len(api.calls) != 1 {
		t.Fatalf("must replace old media safely: %v %d", err, len(api.calls))
	}
}

func TestExplicitMediaRejectionFallsBackButUnknownDoesNot(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		api := &rejectingMediaAPI{unknown: unknown}
		a := New(api, t.TempDir(), nil)
		result, err := a.Apply(context.Background(), "123:test", rt.Message{Purpose: "panel", Text: "keep buying", PhotoURL: "https://example.invalid/photo.png"})
		if unknown {
			if err == nil || len(api.calls) != 1 {
				t.Fatal("unknown photo send caused duplicate")
			}
		} else {
			if err != nil || len(api.calls) != 2 || result.MessageKind != "text" || result.MessageID != 9 || api.calls[1].Text != "keep buying" {
				t.Fatal("definite rejection did not yield text panel")
			}
		}
	}
}

type mediaCacheFake struct {
	bot              uint
	path, hash, file string
}

func (f *mediaCacheFake) CachedMedia(_ context.Context, b uint, p, h string) (string, error) {
	if b == f.bot && p == f.path && h == f.hash {
		return f.file, nil
	}
	return "", nil
}
func (f *mediaCacheFake) CacheMedia(_ context.Context, b uint, p, h, v string) error {
	f.bot = b
	f.path = p
	f.hash = h
	f.file = v
	return nil
}
func TestProductMediaCacheAndFallback(t *testing.T) {
	root := t.TempDir()
	ref := testPhoto(t, root)
	api := &mediaAPIFake{}
	cache := &mediaCacheFake{}
	a := New(api, root, cache)
	msg := rt.Message{Purpose: "panel", Operation: "edit", PhotoURL: ref, Text: "product"}
	if _, err := a.Apply(context.Background(), "123:test", msg); err != nil {
		t.Fatal(err)
	}
	if len(api.calls[0].Photo) == 0 || api.calls[0].PhotoURL != "" {
		t.Fatal("local file not resolved")
	}
	if _, err := a.Apply(context.Background(), "123:test", msg); err != nil {
		t.Fatal(err)
	}
	if api.calls[1].FileID != "file-one" || len(api.calls[1].Photo) != 0 {
		t.Fatal("cache not used")
	}
	if _, err := a.Apply(context.Background(), "456:test", msg); err != nil {
		t.Fatal(err)
	}
	if len(api.calls[2].Photo) == 0 {
		t.Fatal("file id leaked across bots")
	}
	msg.PhotoURL = "/uploads/missing.png"
	if _, err := a.Apply(context.Background(), "123:test", msg); err != nil {
		t.Fatal(err)
	}
	if api.calls[3].PhotoURL != "" || api.calls[3].Text != "product" {
		t.Fatal("missing photo blocks text")
	}
	msg.PhotoURL = "https://example.invalid/product.png"
	if _, err := a.Apply(context.Background(), "123:test", msg); err != nil {
		t.Fatal(err)
	}
	if api.calls[4].PhotoURL != msg.PhotoURL {
		t.Fatal("external URL must be passed to Telegram")
	}
	api.err = errors.New("unknown")
	before := len(api.calls)
	if _, err := a.Apply(context.Background(), "123:test", msg); err == nil {
		t.Fatal("unknown hidden")
	}
	if len(api.calls) != before+1 {
		t.Fatal("unknown send retried")
	}
}

func TestProductMediaForwardsBotProfile(t *testing.T) {
	var called bool
	api := New(profileForwardStub{called: &called}, t.TempDir(), nil)
	if _, ok := any(api).(rt.ProfileAPI); !ok {
		t.Fatal("productmedia.API must implement ProfileAPI")
	}
	if err := api.ApplyBotProfile(context.Background(), "123:token", rt.BotProfile{Name: "Shop"}); err != nil || !called {
		t.Fatalf("forward: called=%v err=%v", called, err)
	}
}

type profileForwardStub struct {
	rt.API
	called *bool
}

func (s profileForwardStub) ApplyBotProfile(context.Context, string, rt.BotProfile) error {
	*s.called = true
	return nil
}
