package authz

import "testing"

// TestMembershipProductOperationsRBAC 验证生产种子授予运营 PATCH/DELETE，但不向只读/客服/财务扩权。
func TestMembershipProductOperationsRBAC(t *testing.T) {
	service := setupAuthzServiceTest(t)
	if err := service.BootstrapBuiltinRoles(); err != nil {
		t.Fatal(err)
	}
	for index, role := range []string{"operations", "readonly_auditor", "support", "finance"} {
		id := uint(index + 1)
		if err := service.SetAdminRoles(id, []string{role}); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"PATCH", "DELETE"} {
			allowed, err := service.EnforceAdmin(id, "/api/v1/admin/telegram-bot/membership/products/12", method)
			if err != nil || allowed != (role == "operations") {
				t.Fatalf("%s %s allowed=%v err=%v", role, method, allowed, err)
			}
		}
	}
}
