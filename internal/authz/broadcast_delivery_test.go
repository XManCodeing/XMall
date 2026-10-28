package authz

import "testing"

func TestBroadcastDeliveryPolicies(t *testing.T) {
	s := setupAuthzServiceTest(t)
	if err := s.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	for id, role := range []string{"system_admin", "readonly_auditor", "operations", "support", "finance", "integration"} {
		if err := s.SetAdminRoles(uint(id+1), []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, route := range []struct{ path, method string }{
			{"/api/v1/admin/telegram-bot/broadcasts/4/deliveries", "GET"},
			{"/api/v1/admin/telegram-bot/broadcasts/4/deliveries/8/retry", "POST"},
		} {
			allow, err := s.EnforceAdmin(uint(id+1), route.path, route.method)
			if err != nil || allow != (role == "system_admin") {
				t.Fatalf("%s %s: allowed=%v error=%v", role, route.path, allow, err)
			}
		}
	}
	allow, err := s.EnforceAdmin(1, "/admin/telegram-bot/broadcasts/4/deliveries/8/retry", "GET")
	if err != nil || allow {
		t.Fatalf("wrong method allowed: %v %v", allow, err)
	}
}
