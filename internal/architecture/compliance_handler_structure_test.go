package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The declaration feature was removed, rather than relocated. Keep both its
// former module and legacy entry points absent; other module guards are unchanged.
func TestComplianceModuleStaysRemoved(t *testing.T) {
	root := findRepositoryRoot(t)
	moduleRoot := filepath.Join(root, "internal/modules/compliance")
	if err := filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Errorf("removed compliance module must not contain Go source: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{
		"internal/http/handlers/admin/admin_compliance.go",
		"internal/service/compliance_service.go",
		"internal/app/httpserver/middleware/compliance_middleware.go",
	} {
		if _, err := os.Stat(filepath.Join(root, removed)); !os.IsNotExist(err) {
			t.Errorf("compliance entry point must stay removed: %s (stat: %v)", removed, err)
		}
	}
	for _, file := range []string{
		"internal/app/httpserver/routes_admin.go",
		"internal/app/container/container.go",
		"internal/app/container/services_foundation.go",
		"internal/authz/bootstrap.go",
	} {
		source, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(source)), "compliance") {
			t.Errorf("removed compliance wiring must not return in %s", file)
		}
	}
}
