package container

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	supportbridge "github.com/XManCodeing/XMall/internal/bootstrap/telegramsupportrecovery"
	recoveryapp "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application"
	botreplacement "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/application/botreplacement"
	recoveryhttp "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/transport/http"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/contract"
	"github.com/gin-gonic/gin"
)

func TestContainerAssemblyIsSplitByResponsibility(t *testing.T) {
	directory := currentContainerDirectory(t)
	tests := []struct {
		file     string
		required []string
	}{
		{file: "bootstrap.go", required: []string{"func NewContainer(", "func newPaymentProviderRegistry("}},
		{file: "repositories.go", required: []string{"func (c *Container) initRepositories()"}},
		{file: "services.go", required: []string{"func (c *Container) initServices()"}},
		{file: "services_foundation.go", required: []string{
			"func (c *Container) initPolicyAndSettingServices()",
			"func (c *Container) loadRuntimeSettings()",
			"func (c *Container) initIdentityAndCatalogServices()",
		}},
		{file: "services_application.go", required: []string{"func (c *Container) initApplicationServices()"}},
		{file: "services_integration.go", required: []string{"func (c *Container) initIntegrationServices()"}},
		{file: "services_wiring.go", required: []string{"func (c *Container) wireServiceDependencies()"}},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			source := readContainerSource(t, filepath.Join(directory, test.file))
			for _, required := range test.required {
				if !strings.Contains(source, required) {
					t.Errorf("%s must contain %q", test.file, required)
				}
			}
		})
	}

	containerSource := readContainerSource(t, filepath.Join(directory, "container.go"))
	for _, forbidden := range []string{
		"func NewContainer(",
		"func (c *Container) initRepositories()",
		"func (c *Container) initServices()",
	} {
		if strings.Contains(containerSource, forbidden) {
			t.Errorf("container.go must only declare the dependency surface; move %q to its assembly file", forbidden)
		}
	}
}

func TestServiceAssemblyDeclaresDependencyOrder(t *testing.T) {
	directory := currentContainerDirectory(t)
	source := readContainerSource(t, filepath.Join(directory, "services.go"))
	orderedCalls := []string{
		"c.initPolicyAndSettingServices()",
		"c.loadRuntimeSettings()",
		"c.initIdentityAndCatalogServices()",
		"c.initApplicationServices()",
		"c.initIntegrationServices()",
		"c.wireServiceDependencies()",
	}

	previous := -1
	for _, call := range orderedCalls {
		index := strings.Index(source, call)
		if index < 0 {
			t.Fatalf("services.go must call %s", call)
		}
		if index <= previous {
			t.Fatalf("service assembly call %s is out of dependency order", call)
		}
		previous = index
	}
}

func TestTelegramTransactionalDeliveryIsWired(t *testing.T) {
	directory := currentContainerDirectory(t)
	for file, required := range map[string][]string{
		"repositories.go": {
			"ordergormstore.New(db, c.Config.App.SecretKey).EnableBusinessEvents()",
			"paymentgormstore.New(db, c.Config.App.SecretKey).EnableBusinessEvents()",
			"broadcaststore.New(db).WithBotRecoveryGuard(",
			"GuardRecoveryWrite(tx.Statement.Context, bot)",
		},
		"services_integration.go": {
			"NewLifecycle(c.QueueClient, c.SettingService, c.Config.Email).EnableBusinessEvents()",
		},
		"telegram_bot.go": {
			"broadcastapp.NewRuntimeService(",
			"c.TelegramBroadcastService.Reconcile(ctx)",
			"dispatcher.Reconcile(ctx, botID)",
			"supportStore.WithPolicyRecoveryGuard(",
			"recoveryAccess := c.initMembershipGroupRecovery(platform, identity)",
			"Groups: groupsstore.New(gormdb.DB), Recovery: recoveryAccess}",
			"Bots: runtimeStore, Recovery: recoveryAccess}",
			"membershipbridge.NewGroup(store, platform, c.TelegramRecovery.Store, c.authorizeGroupRecovery, identity)",
			"c.TelegramGroupRecovery.Membership = recoverer",
			"handler.WithSupportManagement(container.TelegramSupportRecovery)",
			"container.TelegramSupportRecovery = supportbridge.New(gormdb.DB, container.TelegramBotManager, runtimeapi.Adapter{}, container.authorizeGroupRecovery)",
			"c.initSupportGroupRecovery()",
			"c.TelegramGroupRecovery.ContinueSupport =",
			"adapter.ContinueGroup(ctx, request.ActorID, request, c.TelegramBotRuntime)",
			"adapter.RefreshGroup(ctx, request.ActorID, request, probe)",
			"botRecoverer := c.initBotRecoveryModules(platform, identity)",
			"membershipbridge.NewBotAdmission(store, platform, c.TelegramRecovery.Store, c.authorizeGroupRecovery, identity)",
			"Worker: recoverer, BotWorker: botRecoverer}",
			"c.TelegramRecovery.Bots.Modules = botRecoveryModules{",
			"&membershipbridge.Modules{Store: store, Recovery: recoverer}",
			"supportbridge.New(gormdb.DB, c.TelegramBotManager, runtimeapi.Adapter{}, c.authorizeGroupRecovery)",
		},
	} {
		source := readContainerSource(t, filepath.Join(directory, file))
		for _, text := range required {
			if !strings.Contains(source, text) {
				t.Errorf("%s missing transactional Telegram wiring: %s", file, text)
			}
		}
	}
}

// TestSupportRecoveryManagementRoutesAreAssembled checks production handler wiring.
// Missing ports expose no management routes; installing the real adapter adds all three.
func TestSupportRecoveryManagementRoutesAreAssembled(t *testing.T) {
	for _, installed := range []bool{false, true} {
		container := &Container{TelegramRecovery: &recoveryapp.Service{}}
		if installed {
			container.TelegramSupportRecovery = supportbridge.New(nil, nil, nil, nil)
		}
		router := gin.New()
		recoveryhttp.RegisterRoutes(router, container.TelegramRecoveryHandler())
		found := make(map[string]bool)
		for _, route := range router.Routes() {
			found[route.Method+" "+route.Path] = true
		}
		for _, route := range []string{
			"GET /telegram-bot/recovery/tasks/:id/support-entries",
			"GET /telegram-bot/recovery/tasks/:id/support-entries/:entry/operations",
			"POST /telegram-bot/recovery/tasks/:id/support-entries/:entry/resolve",
		} {
			if found[route] != installed {
				t.Fatalf("route %s installed=%t present=%t", route, installed, found[route])
			}
		}
	}
}

type recoveryModuleFunc func(context.Context, botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error)

// ContinueBot delegates a synthetic module call and returns its proof or error.
func (module recoveryModuleFunc) ContinueBot(ctx context.Context, request botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error) {
	return module(ctx, request)
}

// TestBotRecoveryModulesPreservesIndependentProofs checks that incomplete evidence
// is passed unchanged to the parent and an adapter error yields no release proof.
func TestBotRecoveryModulesPreservesIndependentProofs(t *testing.T) {
	request := botreplacement.ModuleRequest{TaskID: 7, ActorID: 3}
	var calls []string
	member := recoveryModuleFunc(func(_ context.Context, actual botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error) {
		if actual.TaskID != request.TaskID || actual.ActorID != request.ActorID {
			t.Fatal("module identity changed")
		}
		calls = append(calls, "membership")
		return []botreplacement.ModuleProof{{Module: "membership", Receipt: "member-receipt", LocalComplete: true}}, nil
	})
	support := recoveryModuleFunc(func(_ context.Context, actual botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error) {
		if actual.TaskID != request.TaskID || actual.ActorID != request.ActorID {
			t.Fatal("support identity changed")
		}
		calls = append(calls, "support")
		return []botreplacement.ModuleProof{{Module: "support", Receipt: "support-receipt", Remaining: 2, Unknown: 1}}, nil
	})
	proofs, err := (botRecoveryModules{member, support}).ContinueBot(context.Background(), request)
	if err != nil || len(proofs) != 2 || strings.Join(calls, ",") != "membership,support" {
		t.Fatalf("independent modules not executed: %+v %v %v", proofs, calls, err)
	}
	if proofs[1].LocalComplete || proofs[1].Remaining != 2 || proofs[1].Unknown != 1 {
		t.Fatalf("incomplete support proof was upgraded: %+v", proofs[1])
	}
	injected := errors.New("synthetic module failure")
	failure := recoveryModuleFunc(func(context.Context, botreplacement.ModuleRequest) ([]botreplacement.ModuleProof, error) {
		return nil, injected
	})
	for _, modules := range []botRecoveryModules{{member, failure}, {failure, support}} {
		if result, err := modules.ContinueBot(context.Background(), request); !errors.Is(err, injected) || len(result) != 0 {
			t.Fatalf("module failure produced usable evidence: %+v %v", result, err)
		}
	}
	for _, modules := range []botRecoveryModules{{member, nil}, {nil, support}} {
		if _, err := modules.ContinueBot(context.Background(), request); !errors.Is(err, botreplacement.ErrContinuationRequired) {
			t.Fatalf("missing module did not fail closed: %v", err)
		}
	}
}

func currentContainerDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve container test filename")
	}
	return filepath.Dir(filename)
}

func readContainerSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read container source %s: %v", path, err)
	}
	return string(raw)
}

// TestProductionPublicSaleGuardsAreRequired 防止生产构造遗漏显式保护开关。
func TestProductionPublicSaleGuardsAreRequired(t *testing.T) {
	cases := []struct{ file, options string }{
		{"services_application.go", "OrderServiceOptions"},
		{"services_integration.go", "PaymentServiceOptions"},
		{"services_foundation.go", "Dependencies"},
	}
	for _, test := range cases {
		t.Run(test.file, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), test.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				selector, ok := literal.Type.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != test.options {
					return true
				}
				for _, element := range literal.Elts {
					field, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := field.Key.(*ast.Ident)
					if !ok || key.Name != "EnforcePublicSale" {
						continue
					}
					value, ok := field.Value.(*ast.Ident)
					found = ok && value.Name == "true"
				}
				return true
			})
			if !found {
				t.Fatal("production must explicitly enable public sale guard")
			}
		})
	}
}

// TestRecoveryLeasePanicJoins verifies both renewal panic and work unwinding
// cancel/join before release; panic payloads must never become returned errors.
func TestRecoveryLeasePanicJoins(t *testing.T) {
	for _, workPanics := range []bool{false, true} {
		t.Run(fmt.Sprint(workPanics), func(t *testing.T) {
			entered := make(chan struct{})
			var renewalContext context.Context
			lease := &recoveryLease{renew: func(ctx context.Context) error {
				renewalContext = ctx
				close(entered)
				if workPanics {
					<-ctx.Done()
					return ctx.Err()
				}
				panic("secret renewal payload")
			}}
			completed := make(chan error, 1)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						if recovered != "work panic" {
							t.Error("unexpected work panic")
						}
						completed <- nil
					}
				}()
				completed <- withRecoveryLease(context.Background(), recoveryLeaser{lease}, func(ctx context.Context) error {
					<-entered
					if workPanics {
						panic("work panic")
					}
					<-ctx.Done()
					if lease.released.Load() {
						t.Error("released before work joined")
					}
					return nil // A late success cannot hide loss of ownership.
				}, time.Millisecond, time.Second, time.Second)
			}()
			select {
			case err := <-completed:
				if !workPanics && (err == nil || strings.Contains(err.Error(), "secret")) {
					t.Fatalf("unsafe renewal error: %v", err)
				}
				if !lease.released.Load() || renewalContext.Err() == nil {
					t.Fatal("lease or renewal context not cleaned up")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("panic cleanup failed to join")
			}
		})
	}
}

type recoveryLease struct {
	renew    func(context.Context) error
	released atomic.Bool
}

func (l *recoveryLease) Renew(ctx context.Context) error { return l.renew(ctx) }
func (l *recoveryLease) Release(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	l.released.Store(true)
	return nil
}

type recoveryLeaser struct{ lease *recoveryLease }

func (l recoveryLeaser) Acquire(context.Context, string) (c.Lease, error) { return l.lease, nil }
func TestRecoveryLeaseRenewsSlowWorkAndJoins(t *testing.T) {
	var renewals atomic.Int32
	lease := &recoveryLease{renew: func(context.Context) error { renewals.Add(1); return nil }}
	err := withRecoveryLease(context.Background(), recoveryLeaser{lease}, func(ctx context.Context) error {
		timer := time.NewTimer(140 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if lease.released.Load() {
			t.Error("lease released before recovery joined")
		}
		return nil
	}, 5*time.Millisecond, 20*time.Millisecond, 70*time.Millisecond)
	if err != nil || renewals.Load() < 3 || !lease.released.Load() {
		t.Fatalf("err=%v renewals=%d released=%v", err, renewals.Load(), lease.released.Load())
	}
}
func TestRecoveryLeaseFailureCancelsWork(t *testing.T) {
	lost := errors.New("lease lost")
	lease := &recoveryLease{renew: func(context.Context) error { return lost }}
	err := withRecoveryLease(context.Background(), recoveryLeaser{lease}, func(ctx context.Context) error {
		<-ctx.Done()
		if lease.released.Load() {
			t.Error("released before cancellation joined")
		}
		return ctx.Err()
	}, time.Millisecond, 20*time.Millisecond, time.Second)
	if !errors.Is(err, lost) || !lease.released.Load() {
		t.Fatalf("err=%v", err)
	}
}
func TestRecoveryLeaseDeadlineAndShutdownPropagate(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{true: "shutdown", false: "stalled renewal"}[shutdown], func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			lease := &recoveryLease{renew: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
			done := make(chan error, 1)
			go func() {
				done <- withRecoveryLease(parent, recoveryLeaser{lease}, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, time.Millisecond, time.Second, 40*time.Millisecond)
			}()
			<-entered
			if shutdown {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !lease.released.Load() {
					t.Fatalf("err=%v released=%v", err, lease.released.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation failed to join recovery/renewal")
			}
		})
	}
}
