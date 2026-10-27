package version

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrUpdateSourceNotConfigured = errors.New("update repository is not configured")
	ErrInvalidUpdateSource       = errors.New("invalid update repository")
	repositoryOwnerPattern       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repositoryNamePattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
)

// ValidateRepository accepts only a GitHub owner/repo, never a URL or path.
// An empty source disables remote checks and updates; there is no fallback.
func ValidateRepository(repository string) error {
	if repository == "" {
		return ErrUpdateSourceNotConfigured
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !repositoryOwnerPattern.MatchString(parts[0]) ||
		strings.Contains(parts[0], "--") || !repositoryNamePattern.MatchString(parts[1]) {
		return ErrInvalidUpdateSource
	}
	return nil
}

// ValidateReleaseAssetURL binds initial downloads to the configured repository,
// release tag and asset name. Only subsequent redirects may use GitHub's CDN.
func ValidateReleaseAssetURL(rawURL, repository, tag, name string) error {
	if err := ValidateRepository(repository); err != nil {
		return err
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
		return errors.New("invalid release asset name")
	}
	if _, err := parseSemver(tag); err != nil {
		return fmt.Errorf("invalid release tag: %w", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	expectedPath := "/" + repository + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.EscapedPath() != expectedPath {
		return errors.New("release asset URL does not match configured repository and release")
	}
	return nil
}
