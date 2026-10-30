package productmedia

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rt "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/XManCodeing/XMall/internal/modules/telegram/runtime/infrastructure/botapi"
	telegram "github.com/XManCodeing/XMall/internal/platform/telegramapi"
)

func TestAdapterLocalUploadReturnsReusableFileID(t *testing.T) {
	root := t.TempDir()
	ref := testPhoto(t, root)
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			t.Errorf("unexpected operation %s", r.URL.Path)
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(11 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, _, err := r.FormFile("photo")
			if err != nil {
				t.Error(err)
				return
			}
			file.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":9,"photo":[{"file_id":"small","width":1,"height":1},{"file_id":"photo-cached","width":8,"height":8}]}}`)
	}))
	defer server.Close()
	cache := &mediaCacheFake{}
	api := New(botapi.Adapter{Options: []telegram.Option{telegram.WithBaseURL(server.URL), telegram.WithHTTPClient(server.Client())}}, root, cache)
	for i := 0; i < 2; i++ {
		result, err := api.Apply(context.Background(), "123:test", rt.Message{Purpose: "panel", Operation: "send", ChatID: 22, PhotoURL: ref, Text: "product"})
		if err != nil {
			t.Fatal(err)
		}
		if result.MessageID != 9 || result.FileID != "photo-cached" || result.MessageKind != "photo" {
			t.Fatalf("incomplete media result: %+v", result)
		}
	}
	if len(methods) != 2 || cache.file != "photo-cached" {
		t.Fatal("media cache not persisted")
	}
}
