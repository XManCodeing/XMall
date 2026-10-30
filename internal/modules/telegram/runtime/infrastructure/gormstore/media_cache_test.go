package gormstore

import (
	d "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"testing"
)

func TestMediaCacheBotContentIsolation(t *testing.T) {
	s, db, ctx, _ := setup(t)
	must(t, db.AutoMigrate(&d.MediaCache{}))
	must(t, s.CacheMedia(ctx, 123, "path", "hash-a", "file-a"))
	file, err := s.CachedMedia(ctx, 123, "path", "hash-a")
	must(t, err)
	if file != "file-a" {
		t.Fatal("cache miss")
	}
	for _, key := range []struct {
		bot        uint
		path, hash string
	}{{456, "path", "hash-a"}, {123, "path", "hash-b"}, {123, "other", "hash-a"}} {
		file, err = s.CachedMedia(ctx, key.bot, key.path, key.hash)
		must(t, err)
		if file != "" {
			t.Fatal("cache crossed bot/path/content")
		}
	}
	must(t, s.CacheMedia(ctx, 123, "path", "hash-a", "file-new"))
	file, err = s.CachedMedia(ctx, 123, "path", "hash-a")
	must(t, err)
	if file != "file-new" {
		t.Fatal("cache refresh failed")
	}
}
