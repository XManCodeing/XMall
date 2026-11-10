package membershiphttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

// accessStub records the actual authenticated inputs without making external calls.
type accessStub struct {
	account, cursor, target uint
	limit, lists, requests  int
	err                     error
}

// List returns a safe read projection and captures account/cursor/limit for authorization assertions.
func (stub *accessStub) List(_ context.Context, account, cursor uint, limit int) (GroupPage, error) {
	stub.account, stub.cursor, stub.limit = account, cursor, limit
	stub.lists++
	return GroupPage{}, stub.err
}

// Request records the server-derived account and target; body identity fields are intentionally ignored.
func (stub *accessStub) Request(_ context.Context, account, target uint) (InvitationResult, error) {
	stub.account, stub.target = account, target
	stub.requests++
	return InvitationResult{State: "processing"}, stub.err
}

// accessRouter creates an isolated route harness; authenticated IDs model the existing JWT boundary.
func accessRouter(access UserAccess, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")
	if authenticated {
		group.Use(func(ctx *gin.Context) { ctx.Set("user_id", uint(42)); ctx.Next() })
	}
	RegisterUserRoutes(group, NewUserHandler(access))
	return router
}

// accessRequest runs one HTTP request and decodes the standard response envelope.
func accessRequest(t *testing.T, router http.Handler, method, path, body string) (*httptest.ResponseRecorder, response.Response) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var result response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode membership response: %v", err)
	}
	return recorder, result
}

// TestUserMembershipReadDoesNotGenerateInvitation verifies read-only lists and server-owned identity.
func TestUserMembershipReadDoesNotGenerateInvitation(t *testing.T) {
	stub := &accessStub{}
	recorder, result := accessRequest(t, accessRouter(stub, true), "GET", "/api/v1/user/membership-groups?cursor=7&limit=10&account_id=99&telegram_id=100", "")
	if result.StatusCode != 0 || stub.account != 42 || stub.cursor != 7 || stub.limit != 10 || stub.requests != 0 || stub.lists != 1 {
		t.Fatalf("unexpected authorized read: result=%+v stub=%+v", result, stub)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(recorder.Body.String(), `"items":[]`) {
		t.Fatal("membership data must not be cached and empty collections must be arrays")
	}
}

// TestUserMembershipRequestCannotChooseIdentity verifies explicit action uses only authenticated identity.
func TestUserMembershipRequestCannotChooseIdentity(t *testing.T) {
	stub := &accessStub{}
	recorder, result := accessRequest(t, accessRouter(stub, true), "POST", "/api/v1/user/membership-groups/8/invitation", `{"account_id":99,"telegram_id":"123","bot_id":5}`)
	if result.StatusCode != 0 || stub.account != 42 || stub.target != 8 || stub.requests != 1 || stub.lists != 0 {
		t.Fatalf("unexpected invitation request: result=%+v stub=%+v", result, stub)
	}
	if recorder.Header().Get("Referrer-Policy") != "no-referrer" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invitation response must prevent caching and referrer disclosure")
	}
}

// TestUserMembershipInvalidInputsNeverReachService rejects invalid IDs, unbounded limits, and anonymous requests.
func TestUserMembershipInvalidInputsNeverReachService(t *testing.T) {
	cases := []struct{ method, path string }{
		{"GET", "/api/v1/user/membership-groups?cursor=-1"},
		{"GET", "/api/v1/user/membership-groups?cursor=01"},
		{"GET", "/api/v1/user/membership-groups?limit=101"},
		{"GET", "/api/v1/user/membership-groups?limit=0"},
		{"POST", "/api/v1/user/membership-groups/0/invitation"},
		{"POST", "/api/v1/user/membership-groups/-1/invitation"},
		{"POST", "/api/v1/user/membership-groups/18446744073709551616/invitation"},
	}
	for _, sample := range cases {
		stub := &accessStub{}
		_, result := accessRequest(t, accessRouter(stub, true), sample.method, sample.path, "{}")
		if result.StatusCode == 0 || stub.lists != 0 || stub.requests != 0 {
			t.Fatalf("invalid request reached service: %s", sample.path)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/user/membership-groups"
		if method == "POST" {
			path += "/8/invitation"
		}
		stub := &accessStub{}
		_, result := accessRequest(t, accessRouter(stub, false), method, path, "{}")
		if result.StatusCode != response.CodeUnauthorized || stub.lists != 0 || stub.requests != 0 {
			t.Fatal("anonymous request must be denied before business access")
		}
	}
}

// TestUserMembershipErrorsHideDiagnostics ensures persistence and platform secrets cannot enter HTTP errors.
func TestUserMembershipErrorsHideDiagnostics(t *testing.T) {
	for _, err := range []error{ErrAccessDenied, errors.New("token-secret invitation-secret database diagnostics")} {
		recorder, result := accessRequest(t, accessRouter(&accessStub{err: err}, true), "POST", "/api/v1/user/membership-groups/8/invitation", "{}")
		if result.StatusCode == 0 || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "diagnostics") {
			t.Fatal("membership error leaked diagnostics or returned success")
		}
	}
	_, result := accessRequest(t, accessRouter(nil, true), "GET", "/api/v1/user/membership-groups", "")
	if result.StatusCode == 0 {
		t.Fatal("missing access dependency must fail closed")
	}
}
