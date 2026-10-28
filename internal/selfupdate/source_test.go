package selfupdate

import (
	"context"
	"errors"
	"testing"

	"github.com/homles698698-lab/XMall/internal/version"
)

func TestDisabledManagerNeverDetectsOrFetches(t *testing.T) {
	for _, repository := range []string{"", "https://github.com/example/releases"} {
		m := NewManager(repository)
		m.detect = func() Capability { t.Fatal("disabled updater probed environment"); return Capability{} }
		m.fetchLatest = func(context.Context) (*version.Release, error) {
			t.Fatal("disabled updater fetched release")
			return nil, nil
		}
		err := m.Start(context.Background())
		if !errors.Is(err, version.ErrUpdateSourceNotConfigured) && !errors.Is(err, version.ErrInvalidUpdateSource) {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Running() || m.Snapshot().Status != StatusIdle {
			t.Fatal("disabled updater started work")
		}
	}
}

func TestDisabledUpdaterRejectsBeforeFilesystemChanges(t *testing.T) {
	if err := NewUpdater().applyLocked(context.Background(), &version.Release{}, "/must-not-be-touched/app", nil); !errors.Is(err, version.ErrUpdateSourceNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestSelectAssetsRejectsAmbiguity(t *testing.T) {
	archive := version.Asset{Name: archivePrefix + "v1.2.3" + PlatformAssetSuffix(), DownloadURL: "https://github.com/archive"}
	checksum := version.Asset{Name: "checksums.txt", DownloadURL: "https://github.com/sums"}
	for _, assets := range [][]version.Asset{{archive, archive, checksum}, {archive, checksum, checksum}} {
		if _, _, _, err := selectAssets(assets); err == nil {
			t.Fatal("ambiguous release assets accepted")
		}
	}
}

func TestUpdaterRejectsWrongRepositoryBeforeDownload(t *testing.T) {
	u := NewUpdater("example/releases")
	release := &version.Release{TagName: "v1.2.3", Assets: []version.Asset{{Name: "checksums.txt", DownloadURL: "https://github.com/other/releases/releases/download/v1.2.3/checksums.txt"}}}
	if err := u.applyLocked(context.Background(), release, "/must-not-be-touched/app", nil); err == nil {
		t.Fatal("accepted other repository")
	}
}
