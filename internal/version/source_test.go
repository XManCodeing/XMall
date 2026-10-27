package version

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type sourceTransport func(*http.Request) (*http.Response, error)

func (f sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestValidateRepository(t *testing.T) {
	for _, source := range []string{"example/releases", "my-org/my.repo_2", "a/b"} {
		if err := ValidateRepository(source); err != nil {
			t.Errorf("%q: %v", source, err)
		}
	}
	for _, source := range []string{"", "https://github.com/a/b", "a/b/c", "a/..", "../repo", "a/b?x=1", "a/b#x", "a/b%2fc", "a/b\\c", " a/b", "a/b ", "a--b/repo", "-a/b", "a-/b", "a/b\n"} {
		if err := ValidateRepository(source); err == nil {
			t.Errorf("accepted %q", source)
		}
	}
}

func TestUnconfiguredSourceNeverConnects(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = sourceTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("unconfigured/invalid update source attempted network access")
		return nil, errors.New("unexpected network")
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, source := range []string{"", "https://evil.invalid/a/b"} {
		if _, err := FetchLatestRelease(context.Background(), source); err == nil {
			t.Fatal("fetch should fail closed")
		}
		if _, err := CheckLatestRelease(context.Background(), source); err == nil {
			t.Fatal("check should fail closed")
		}
	}
}

func TestConfiguredRepositoryAndRedirects(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = sourceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.github.com/repos/example/releases/releases/latest" {
			t.Fatalf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://api.github.com/repos/other/repo/releases/latest"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, err := FetchLatestRelease(context.Background(), "example/releases"); err == nil {
		t.Fatal("redirect must fail closed")
	}
	if calls != 1 {
		t.Fatalf("redirect followed: %d requests", calls)
	}
}

func TestFetchUsesConfiguredRepository(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = sourceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/example/releases/releases/latest" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v9.0.0","html_url":"https://evil.invalid","assets":[{"name":"checksums.txt","browser_download_url":"https://github.com/example/releases/releases/download/v9.0.0/checksums.txt"}]}`)), Header: http.Header{}, Request: r}, nil
	})
	result, err := CheckLatestRelease(context.Background(), "example/releases")
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "https://github.com/example/releases/releases" || result.ReleaseURL != "https://github.com/example/releases/releases/tag/v9.0.0" {
		t.Fatalf("unexpected source: %+v", result)
	}
}

func TestReleaseAssetURLBoundToSource(t *testing.T) {
	base := "https://github.com/example/releases/releases/download/v1.2.3/checksums.txt"
	if err := ValidateReleaseAssetURL(base, "example/releases", "v1.2.3", "checksums.txt"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(base, "https:", "http:", 1),
		strings.Replace(base, "example/releases", "other/releases", 1),
		strings.Replace(base, "github.com", "github.com.evil.invalid", 1),
		strings.Replace(base, "github.com", "github.com:443", 1),
		strings.Replace(base, "github.com", "user@github.com", 1),
		strings.Replace(base, "v1.2.3", "v1.2.2", 1),
		strings.Replace(base, "checksums.txt", "%2e%2e/checksums.txt", 1),
		base + "?download=1", base + "#fragment",
	} {
		if err := ValidateReleaseAssetURL(raw, "example/releases", "v1.2.3", "checksums.txt"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
