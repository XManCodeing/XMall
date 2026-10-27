package main

import (
	"reflect"
	"testing"

	"github.com/dujiao-next/internal/config"
)

func TestWeakRuntimeSecretNamesCoversEveryRootSecret(t *testing.T) {
	cfg := &config.Config{
		App:     config.AppConfig{SecretKey: "change-me-32-byte-secret-key!!"},
		JWT:     config.JWTConfig{SecretKey: "your-secret-key-change-in-production-please"},
		UserJWT: config.JWTConfig{SecretKey: "user-change-me-in-production"},
	}

	want := []string{"app.secret_key", "jwt.secret", "user_jwt.secret"}
	if got := weakRuntimeSecretNames(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("weak runtime secrets want %v got %v", want, got)
	}
}

func TestWeakRuntimeSecretNamesAcceptsStrongIndependentSecrets(t *testing.T) {
	cfg := &config.Config{
		App:     config.AppConfig{SecretKey: "2f8d164772cd4bbcaef8fa4ad19a2a26f7a15505"},
		JWT:     config.JWTConfig{SecretKey: "dd914407e55c4528a393fe522215c18f5fc8687b"},
		UserJWT: config.JWTConfig{SecretKey: "ca36df49b49446d2a9b2cac7f035d11574575b53"},
	}

	if got := weakRuntimeSecretNames(cfg); len(got) != 0 {
		t.Fatalf("strong runtime secrets reported as weak: %v", got)
	}
}

func TestWeakRuntimeSecretNamesRejectsReusedStrongSecrets(t *testing.T) {
	shared := "2f8d164772cd4bbcaef8fa4ad19a2a26f7a15505"
	cfg := &config.Config{
		App:     config.AppConfig{SecretKey: shared},
		JWT:     config.JWTConfig{SecretKey: shared},
		UserJWT: config.JWTConfig{SecretKey: "ca36df49b49446d2a9b2cac7f035d11574575b53"},
	}

	want := []string{"app.secret_key", "jwt.secret"}
	if got := weakRuntimeSecretNames(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("reused runtime secrets want %v got %v", want, got)
	}
}

func TestUnsafeBootstrapAdminPasswordRejectsDefaultsAndPolicyViolations(t *testing.T) {
	cfg := &config.Config{Security: config.SecurityConfig{PasswordPolicy: config.PasswordPolicyConfig{
		MinLength:     10,
		RequireUpper:  true,
		RequireLower:  true,
		RequireNumber: true,
	}}}

	for _, password := range []string{"admin123", "alllowercase1", "NOLOWERCASE1", "NoNumberHere"} {
		if !unsafeBootstrapAdminPassword(cfg, password) {
			t.Errorf("expected bootstrap password %q to be rejected", password)
		}
	}
	if unsafeBootstrapAdminPassword(cfg, "StrongBootstrap123") {
		t.Fatal("expected strong bootstrap password to be accepted")
	}
	if unsafeBootstrapAdminPassword(cfg, "") {
		t.Fatal("empty bootstrap password should keep the skip-initialization behavior")
	}
}
