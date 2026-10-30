package telegramapi

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestProfileMethods(t *testing.T) {
	var sawPhoto, sawAnimated bool
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot123:dummy_secret/")
		switch method {
		case "setMyName", "setMyDescription", "setMyShortDescription", "removeMyProfilePhoto":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Error("expected json")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			switch method {
			case "setMyName":
				if body["name"] != "Shop" || body["language_code"] != "en" {
					t.Errorf("setMyName body: %#v", body)
				}
			case "setMyDescription":
				if body["description"] != "Buy cards" || body["language_code"] != nil && body["language_code"] != "" {
					t.Errorf("setMyDescription body: %#v", body)
				}
			case "setMyShortDescription":
				if body["short_description"] != "Buy cards" {
					t.Errorf("setMyShortDescription body: %#v", body)
				}
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "setMyProfilePhoto":
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
				t.Fatalf("multipart: %v %s", err, r.Header.Get("Content-Type"))
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			form, err := reader.ReadForm(1 << 20)
			if err != nil {
				t.Fatal(err)
			}
			meta := form.Value["photo"]
			if len(meta) != 1 {
				t.Fatalf("photo meta: %#v", meta)
			}
			files := form.File["profile_photo"]
			if len(files) != 1 {
				t.Fatalf("photo file: %#v", form.File)
			}
			f, err := files[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			data, _ := io.ReadAll(f)
			switch {
			case strings.Contains(meta[0], `"type":"static"`) && strings.Contains(meta[0], `"photo":"attach://profile_photo"`):
				if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 {
					t.Fatalf("expected jpeg bytes")
				}
				sawPhoto = true
			case strings.Contains(meta[0], `"type":"animated"`) && strings.Contains(meta[0], `"animation":"attach://profile_photo"`) && strings.Contains(meta[0], `"main_frame_timestamp":1.5`):
				if files[0].Header.Get("Content-Type") != "video/mp4" || string(data[4:8]) != "ftyp" {
					t.Fatalf("expected mp4 part")
				}
				sawAnimated = true
			default:
				t.Fatalf("photo meta: %#v", meta)
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			t.Fatalf("unexpected %s", method)
		}
	})
	ctx := context.Background()
	if err := client.SetMyName(ctx, "Shop", "en"); err != nil {
		t.Fatal(err)
	}
	if err := client.SetMyDescription(ctx, "Buy cards", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.SetMyShortDescription(ctx, "Buy cards", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveMyProfilePhoto(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.SetMyProfilePhoto(ctx, []byte{0xff, 0xd8, 0xff, 0x00}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetMyAnimatedProfilePhoto(ctx, []byte("\x00\x00\x00\x18ftypisom0000"), 1.5); err != nil {
		t.Fatal(err)
	}
	if !sawPhoto || !sawAnimated {
		t.Fatal("profile photo not uploaded")
	}
	if err := client.SetMyAnimatedProfilePhoto(ctx, []byte{0xff, 0xd8, 0xff, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}, 0); err == nil {
		t.Fatal("expected mp4 validation")
	}
	if err := client.SetMyName(ctx, strings.Repeat("a", 65), ""); err == nil {
		t.Fatal("expected name length validation")
	}
	if err := client.SetMyProfilePhoto(ctx, []byte("PNG")); err == nil {
		t.Fatal("expected jpeg validation")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("  你好世界  ", 2); got != "你好" {
		t.Fatalf("got %q", got)
	}
}
