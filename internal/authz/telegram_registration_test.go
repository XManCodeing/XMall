package authz

import "testing"

// TestTelegramBotRegistrationPolicies verifies that the independent registration
// section inherits only system-admin PATCH access. No parameters or values leave
// this test; failures identify a route, role, or authorization error.
func TestTelegramBotRegistrationPolicies(t *testing.T) {
	service := setupAuthzServiceTest(t)
	if err := service.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	roles := []string{"system_admin", "readonly_auditor", "operations", "support", "finance", "integration"}
	for index, role := range roles {
		adminID := uint(index + 1)
		if err := service.SetAdminRoles(adminID, []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"PATCH", "GET", "POST", "PUT", "DELETE"} {
			allowed, err := service.EnforceAdmin(adminID, "/api/v1/admin/settings/telegram-bot/registration", method)
			expected := role == "system_admin" && method == "PATCH"
			if err != nil || allowed != expected {
				t.Fatalf("role=%s method=%s allowed=%v expected=%v error=%v", role, method, allowed, expected, err)
			}
		}
	}
}
