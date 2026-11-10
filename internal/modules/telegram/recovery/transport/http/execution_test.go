package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	groups "github.com/XManCodeing/XMall/internal/modules/telegram/groups/contract"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	bot "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"github.com/gin-gonic/gin"
)

// managementPort records only trusted HTTP identity and emulates stale/denied results.
type managementPort struct {
	c.SupportManagement
	actor, task, entry uint
	err                error
}

func (p *managementPort) ResolveSupport(_ context.Context, actor, task, entry uint, _ c.SupportResolution) error {
	p.actor, p.task, p.entry = actor, task, entry
	return p.err
}

// TestSupportManagementHTTP rejects identity/route injection, unsafe input and
// unauthenticated callers, while mapping late-version errors to conflict.
func TestSupportManagementHTTP(t *testing.T) {
	valid := `{"task_version":"4","revision":"2","operation_id":"8","operation_version":"3","decision":"failed","evidence":"case:checked","thread_id":"0"}`
	for _, test := range []struct {
		name, body string
		auth       bool
		err        error
		status     int
	}{
		{"ok", valid, true, nil, 200}, {"no-auth", valid, false, nil, 401}, {"late", valid, true, c.ErrStale, 409},
		{"actor", strings.TrimSuffix(valid, "}") + `,"actor_id":"9"}`, true, nil, 400},
		{"route", strings.TrimSuffix(valid, "}") + `,"route_version":"9"}`, true, nil, 400},
		{"bad-evidence", strings.Replace(valid, "case:checked", "https://secret", 1), true, nil, 400},
		{"numeric-id", strings.Replace(valid, `"operation_id":"8"`, `"operation_id":8`, 1), true, nil, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := &managementPort{err: test.err}
			handler := New(&a.Service{Store: detailStore{kind: d.BotReplace}}).WithSupportManagement(port)
			router := gin.New()
			if test.auth {
				router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			}
			RegisterRoutes(router, handler)
			response := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/telegram-bot/recovery/tasks/5/support-entries/6/resolve", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if !test.auth {
				if !strings.Contains(response.Body.String(), `"status_code":401`) || port.actor != 0 {
					t.Fatal("unauthenticated call reached port")
				}
				return
			}
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.status == 200 && (port.actor != 7 || port.task != 5 || port.entry != 6) {
				t.Fatal("untrusted identity passed")
			}
		})
	}
}

// detailStore provides a safe task detail without granting executor authority.
type detailStore struct {
	c.Store
	kind d.Kind
}

// Get returns a typed preflight fixture for capability-advertisement tests.
func (store detailStore) Get(context.Context, uint) (*c.Detail, error) {
	return &c.Detail{Task: d.Task{ID: 5, Kind: store.kind, PreflightRef: "safe-ref"}}, nil
}

// TestBotPreflightRejectsInjectedRouteAuthority rejects client snapshots and numeric IDs.
// No backend snapshot may execute before strict JSON boundary validation succeeds.
func TestBotPreflightRejectsInjectedRouteAuthority(t *testing.T) {
	service := &bot.Service{Store: detailStore{kind: d.BotReplace}, Backend: &botAssemblyProbe{}, Authorize: func(context.Context, uint, string) error {
		t.Fatal("invalid JSON reached authorization/backend")
		return c.ErrInvalid
	}}
	handler := New(&a.Service{Store: service.Store, Bots: service})
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	RegisterRoutes(router, handler)
	valid := `{"source_bot_id":"1","destination_bot_id":"2","request_key":"test","reason":"synthetic","routes":[{"target_id":"10","chat_id":"-2001"}]}`
	for _, body := range []string{
		strings.TrimSuffix(valid, "}") + `,"actor_id":"9"}`,
		strings.Replace(valid, `"chat_id":"-2001"`, `"chat_id":"-2001","route_version":"4"`, 1),
		strings.Replace(valid, `"target_id":"10"`, `"target_id":10`, 1),
		valid + `{}`,
		strings.Repeat(" ", 33*1024) + valid,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/bot-preflight", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status=%d", response.Code)
		}
	}
}

// TestDetailAdvertisesOnlyAvailableExecution prevents UI from showing actions for
// preview-only deployments or unsupported task kinds.
func TestDetailAdvertisesOnlyAvailableExecution(t *testing.T) {
	for _, kind := range []d.Kind{d.GroupReplace, d.BotReplace, d.IdentityTransfer} {
		for _, assembled := range []bool{false, true} {
			handler := New(&a.Service{Store: detailStore{kind: kind}})
			if assembled {
				handler.WithExecution(Execution{
					Available: func(context.Context, *c.Detail) bool { return true },
					Confirm:   func(context.Context, uint, c.Confirmation) (*d.Task, error) { return nil, c.ErrInvalid },
					Resume:    func(context.Context, uint, uint64, uint) (*d.Task, error) { return nil, c.ErrInvalid },
				})
			}
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			RegisterRoutes(router, handler)
			result := httptest.NewRecorder()
			router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/telegram-bot/recovery/tasks/5", nil))
			var body struct {
				Data struct {
					ExecutionAvailable bool `json:"execution_available"`
				} `json:"data"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.ExecutionAvailable != (assembled && kind == d.GroupReplace) {
				t.Fatalf("kind=%s assembled=%v body=%s", kind, assembled, result.Body.String())
			}
		}
	}
}

// botAssemblyProbe supplies constructor-only interfaces. Tests never execute a
// platform action: an unexpected method call would fail rather than fabricate proof.
type botAssemblyProbe struct {
	bot.Backend
	bot.Drainer
	bot.Continuation
	bot.Modules
	groups.RecoveryProber
}

// TestBotDetailReflectsActualAssembly covers each missing port and complete
// assembly while leaving the persisted task state and all runtime evidence intact.
func TestBotDetailReflectsActualAssembly(t *testing.T) {
	for _, missing := range []string{"none", "store", "backend", "authorize", "drain", "continuation", "probe", "modules", "routes"} {
		t.Run(missing, func(t *testing.T) {
			ports := &botAssemblyProbe{}
			service := &bot.Service{Store: detailStore{kind: d.BotReplace}, Backend: ports, Drain: ports, Continuation: ports, Probe: ports, Modules: ports, Authorize: func(context.Context, uint, string) error { return nil }}
			handler := New(&a.Service{Store: detailStore{kind: d.BotReplace}, Bots: service})
			handler.WithExecution(Execution{
				Confirm: func(context.Context, uint, c.Confirmation) (*d.Task, error) { return nil, c.ErrInvalid },
				Resume:  func(context.Context, uint, uint64, uint) (*d.Task, error) { return nil, c.ErrInvalid },
			})
			switch missing {
			case "store":
				service.Store = nil
			case "backend":
				service.Backend = nil
			case "authorize":
				service.Authorize = nil
			case "drain":
				service.Drain = nil
			case "continuation":
				service.Continuation = nil
			case "probe":
				service.Probe = nil
			case "modules":
				service.Modules = nil
			case "routes":
				handler.execution = Execution{}
			}
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			RegisterRoutes(router, handler)
			result := httptest.NewRecorder()
			router.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/telegram-bot/recovery/tasks/5", nil))
			var body struct {
				Data struct {
					Execution   bool   `json:"execution_available"`
					Preparation bool   `json:"preparation_available"`
					Blocker     string `json:"blocker"`
				} `json:"data"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantPreparation := missing != "store" && missing != "backend" && missing != "authorize"
			if result.Code != 200 || body.Data.Execution != (missing == "none") || body.Data.Preparation != wantPreparation || (body.Data.Blocker == "") != (missing == "none") {
				t.Fatalf("assembly=%s response=%s", missing, result.Body.String())
			}
		})
	}
}

// TestBotConfirmRejectsMissingPortsAtHTTP prevents a caller bypassing the detail capability flag.
// Constructor-only backend methods panic if reached, proving rejection precedes fencing.
func TestBotConfirmRejectsMissingPortsAtHTTP(t *testing.T) {
	for _, missing := range []string{"drain", "continuation", "probe", "modules"} {
		t.Run(missing, func(t *testing.T) {
			ports := &botAssemblyProbe{}
			service := &bot.Service{Store: detailStore{kind: d.BotReplace}, Backend: ports, Drain: ports, Continuation: ports, Probe: ports, Modules: ports, Authorize: func(context.Context, uint, string) error { return nil }}
			switch missing {
			case "drain":
				service.Drain = nil
			case "continuation":
				service.Continuation = nil
			case "probe":
				service.Probe = nil
			case "modules":
				service.Modules = nil
			}
			handler := New(&a.Service{Store: service.Store, Bots: service}).WithExecution(Execution{
				Confirm: service.Confirm, Resume: service.Resume,
			})
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
			RegisterRoutes(router, handler)
			detail, err := service.Store.Get(context.Background(), 5)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]string{"preflight_ref": detail.Task.PreflightRef, "expected_version": "1"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/confirm", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != http.StatusConflict {
				t.Fatalf("missing=%s status=%d body=%s", missing, result.Code, result.Body.String())
			}
		})
	}
}

// executionRouter returns an authenticated test router using only supplied callbacks.
func executionRouter(execution Execution, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if authenticated {
		router.Use(func(ctx *gin.Context) { ctx.Set("admin_id", uint(7)); ctx.Next() })
	}
	RegisterRoutes(router, New(&a.Service{Store: &cancelStore{}}).WithExecution(execution))
	return router
}

// TestCheckpointedResumeReturnsConflict distinguishes pending progress from
// an internal server error without returning an optimistic completion response.
func TestCheckpointedResumeReturnsConflict(t *testing.T) {
	router := executionRouter(Execution{
		Confirm: func(context.Context, uint, c.Confirmation) (*d.Task, error) { return nil, c.ErrInvalid },
		Resume: func(context.Context, uint, uint64, uint) (*d.Task, error) {
			return &d.Task{ID: 5, State: d.Recovering}, a.ErrRecoveryPending
		},
	}, true)
	request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/resume", strings.NewReader(`{"expected_version":"3"}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != http.StatusConflict || !strings.Contains(result.Body.String(), `"error_class":"recovery_pending"`) {
		t.Fatalf("pending checkpoint response: %d %s", result.Code, result.Body.String())
	}
}

// TestExecutionBoundaryPreservesIdentity ensures client-supplied actor and completion
// fields cannot alter the trusted actor or precision of the version passed to execution.
func TestExecutionBoundaryPreservesIdentity(t *testing.T) {
	var confirmed c.Confirmation
	var confirmedID, resumedID, resumedActor uint
	var resumedVersion uint64
	execution := Execution{
		Confirm: func(_ context.Context, id uint, input c.Confirmation) (*d.Task, error) {
			confirmedID, confirmed = id, input
			return &d.Task{ID: id, State: d.Fencing}, nil
		},
		Resume: func(_ context.Context, id uint, version uint64, actor uint) (*d.Task, error) {
			resumedID, resumedVersion, resumedActor = id, version, actor
			return &d.Task{ID: id, State: d.Recovering}, nil
		},
	}
	router := executionRouter(execution, true)
	for _, action := range []string{"confirm", "resume"} {
		request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/"+action,
			strings.NewReader(`{"preflight_ref":"safe-ref","expected_version":"9007199254740993","actor_id":99,"state":"completed"}`))
		request.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		if result.Code != 200 || result.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("action=%s response=%s", action, result.Body.String())
		}
	}
	if confirmedID != 5 || confirmed.ActorID != 7 || confirmed.ExpectedVersion != 9007199254740993 || confirmed.PreflightRef != "safe-ref" {
		t.Fatalf("invalid confirmation identity: %+v id=%d", confirmed, confirmedID)
	}
	if resumedID != 5 || resumedActor != 7 || resumedVersion != 9007199254740993 {
		t.Fatalf("invalid resume identity: %d %d %d", resumedID, resumedActor, resumedVersion)
	}
}

// TestExecutionRejectsInvalidOrUnauthenticatedRequests verifies no callback is
// reached with a missing actor, malformed ID or absent/unsafe version.
func TestExecutionRejectsInvalidOrUnauthenticatedRequests(t *testing.T) {
	calls := 0
	execution := Execution{
		Confirm: func(context.Context, uint, c.Confirmation) (*d.Task, error) { calls++; return nil, c.ErrStale },
		Resume:  func(context.Context, uint, uint64, uint) (*d.Task, error) { calls++; return nil, c.ErrStale },
	}
	for _, authenticated := range []bool{false, true} {
		router := executionRouter(execution, authenticated)
		for _, action := range []string{"confirm", "resume"} {
			for _, body := range []string{`{}`, `{"expected_version":"0"}`, `{"expected_version":9007199254740993}`, `{"expected_version":"-1"}`} {
				request := httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/"+action, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(httptest.NewRecorder(), request)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached executor %d times", calls)
	}
}

// TestIncompleteExecutionAssemblyStaysClosed ensures partial assembly never
// exposes confirm/resume, and no raw execute/complete endpoint exists.
func TestIncompleteExecutionAssemblyStaysClosed(t *testing.T) {
	router := executionRouter(Execution{Confirm: func(context.Context, uint, c.Confirmation) (*d.Task, error) {
		t.Fatal("partial executor called")
		return nil, nil
	}}, true)
	for _, action := range []string{"confirm", "resume", "execute", "complete"} {
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/telegram-bot/recovery/tasks/5/"+action, nil))
		if result.Code != 404 {
			t.Fatalf("partial assembly exposed %s", action)
		}
	}
}
