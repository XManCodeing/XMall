package gormstore_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	a "github.com/XManCodeing/XMall/internal/modules/telegram/membership/application"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	s "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	rd "github.com/XManCodeing/XMall/internal/modules/telegram/runtime/domain"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/url"
)

// TestProductShare resolves every bound target and treats invalid data as per-item
// reasons. t supplies isolated stores; no Telegram requests or schema changes run in production.
func TestProductShare(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		change func(*d.ProductConfig, *rd.Instance) bool
		target map[string]interface{}
	}{
		{name: "valid"},
		{name: "inactive", reason: c.ShareUnavailable, change: func(_ *d.ProductConfig, _ *rd.Instance) bool { return false }},
		{name: "disabled_config", reason: c.ShareUnavailable, change: func(config *d.ProductConfig, _ *rd.Instance) bool { config.Enabled = false; return true }},
		{name: "no_targets", reason: c.ShareMissingBinding, change: func(config *d.ProductConfig, _ *rd.Instance) bool { config.Targets = nil; return true }},
		{name: "missing_target", reason: c.ShareMissingBinding, change: func(config *d.ProductConfig, _ *rd.Instance) bool { config.Targets[1].TargetID = 999; return true }},
		{name: "zero_target", reason: c.ShareMissingBinding, change: func(config *d.ProductConfig, _ *rd.Instance) bool { config.Targets[1].TargetID = 0; return true }},
		{name: "duplicate_target", reason: c.ShareMissingBinding, change: func(config *d.ProductConfig, _ *rd.Instance) bool { config.Targets[1].TargetID = 1; return true }},
		{name: "mixed_bots", reason: c.ShareMixedBots, target: map[string]interface{}{"bot_instance_id": 10}},
		{name: "unbound", reason: c.ShareMissingBinding, target: map[string]interface{}{"bot_instance_id": 0}},
		{name: "wrong_purpose", reason: c.ShareMissingBinding, target: map[string]interface{}{"purpose": "support"}},
		{name: "disabled_target", reason: c.ShareMissingBinding, target: map[string]interface{}{"enabled": false}},
		{name: "invalid_chat", reason: c.ShareMissingBinding, target: map[string]interface{}{"telegram_chat_id": 0}},
		{name: "expired_observation_still_shareable", target: map[string]interface{}{"obs_observed_at": nil, "obs_state": "unavailable"}},
		{name: "runtime_stopped_still_shareable", change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.State = "stopped"; return true }},
		{name: "missing_bot", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.ID = 10; return true }},
		{name: "bot_disabled", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.Enabled = false; return true }},
		{name: "bot_recovery", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.RecoveryTaskID = 1; return true }},
		{name: "external_bot", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.Mode = "external"; return true }},
		{name: "unknown_identity", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.TelegramID = 0; return true }},
		{name: "missing_username", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.Username = ""; return true }},
		{name: "username_injection", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.Username = "mallbot?start=other"; return true }},
		{name: "nonbot_username", reason: c.ShareBotUnavailable, change: func(_ *d.ProductConfig, bot *rd.Instance) bool { bot.Username = "ordinary_user"; return true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, db, _ := setup(t)
			config := &d.ProductConfig{ProductID: 1, Enabled: true, Targets: []d.ProductTarget{{TargetID: 1}, {TargetID: 2}}}
			bot := rd.Instance{ID: 9, TelegramID: 900, Username: "RealMallBot", Enabled: true, Mode: "internal"}
			active := true
			if test.change != nil {
				active = test.change(config, &bot)
			}
			if err := db.AutoMigrate(&rd.Instance{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&bot).Error; err != nil {
				t.Fatal(err)
			}
			if test.target != nil {
				if err := db.Model(&gd.Target{}).Where("id = ?", 2).Updates(test.target).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(&d.ProductShareCode{ProductID: 1, Code: "K7m2Qx9Ba4Nz"}).Error; err != nil {
				t.Fatal(err)
			}
			share, err := store.ProductShare(context.Background(), config, active)
			if err != nil || share.UnavailableReason != test.reason {
				t.Fatalf("share=%+v err=%v", share, err)
			}
			if test.reason != "" {
				if share.URL != "" {
					t.Fatalf("unavailable link exposed URL: %q", share.URL)
				}
				return
			}
			link, err := url.Parse(share.URL)
			if err != nil || link.Scheme != "https" || link.Host != "t.me" || link.Path != "/RealMallBot" {
				t.Fatalf("invalid URL %q: %v", share.URL, err)
			}
			productID, err := store.ResolveProductShare(context.Background(), link.Query().Get("start"))
			if err != nil || productID != 1 {
				t.Fatalf("invalid encrypted link: product=%d err=%v", productID, err)
			}
		})
	}
}

// TestProductShareReadOnlyMissingCode leaves historical products unchanged on reads.
func TestProductShareReadOnlyMissingCode(t *testing.T) {
	store, db, _ := setup(t)
	if err := db.AutoMigrate(&rd.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&rd.Instance{ID: 9, TelegramID: 900, Username: "RealMallBot", Enabled: true, Mode: "internal"}).Error; err != nil {
		t.Fatal(err)
	}
	config := &d.ProductConfig{ProductID: 1, Enabled: true, Targets: []d.ProductTarget{{TargetID: 1}}}
	share, err := store.ProductShare(context.Background(), config, true)
	if err != nil || share.URL != "" || share.UnavailableReason != "" {
		t.Fatalf("missing code: %+v %v", share, err)
	}
	var count int64
	if err := db.Model(&d.ProductShareCode{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("read generated a code", err)
	}
}

// TestProductShareDatabaseFailure ensures real target/bot database failures are not
// downgraded into unavailable links. t owns the deliberately incomplete databases.
func TestProductShareDatabaseFailure(t *testing.T) {
	for _, table := range []string{"telegram_group_targets", "telegram_bot_instances"} {
		t.Run(table, func(t *testing.T) {
			store, db, _ := setup(t)
			if table == "telegram_group_targets" {
				if err := db.Migrator().DropTable(&gd.Target{}); err != nil {
					t.Fatal(err)
				}
			}
			config := &d.ProductConfig{ProductID: 1, Enabled: true, Targets: []d.ProductTarget{{TargetID: 1}}}
			if _, err := store.ProductShare(context.Background(), config, true); err == nil {
				t.Fatal("database failure hidden")
			}
		})
	}
}

// TestShareCodePersistence verifies lazy creation, concurrent reuse, unique indexes and exact resolution.
func TestShareCodePersistence(t *testing.T) {
	store, db, _ := setup(t)
	for _, model := range []any{&rd.Instance{ID: 9, TelegramID: 900, Username: "RealMallBot", Enabled: true, Mode: "internal"}, &d.ProductConfig{ProductID: 1, Enabled: true, Quantity: 1, Version: 1}, &d.ProductTarget{ProductID: 1, TargetID: 1}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&p.Product{}).Where("id = 1").Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	links := make(chan string, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			share, err := store.EnsureProductShare(context.Background(), 1)
			if err != nil {
				t.Error(err)
				return
			}
			links <- share.URL
		}()
	}
	group.Wait()
	close(links)
	var first string
	for link := range links {
		if first == "" {
			first = link
		}
		if link != first {
			t.Fatal("concurrent code changed")
		}
	}
	parsed, err := url.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	code := parsed.Query().Get("start")
	if len(code) != 12 {
		t.Fatalf("invalid code length: %d", len(code))
	}
	for _, character := range code {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z') {
			t.Fatal("non-alphanumeric code")
		}
	}
	id, err := store.ResolveProductShare(context.Background(), code)
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	if err := db.Create(&d.ProductShareCode{ProductID: 2, Code: code}).Error; err == nil {
		t.Fatal("duplicate code accepted")
	}
	for _, invalid := range []string{"product_1", "p1" + code, code + "_", "AAAAAAAAAAAA"} {
		if _, err := store.ResolveProductShare(context.Background(), invalid); !errors.Is(err, c.ErrNotFound) {
			t.Fatal("invalid code accepted", err)
		}
	}
}

// TestTargetNames verifies live names, missing-target tolerance and database failure propagation.
func TestTargetNames(t *testing.T) {
	store, db, _ := setup(t)
	names, err := store.TargetNames(context.Background(), []uint{1, 2, 999})
	if err != nil || len(names) != 2 || names[1] != "member" {
		t.Fatalf("target names: %v %v", names, err)
	}
	if err := db.Model(&gd.Target{}).Where("id = 1").Update("name", "新群名称").Error; err != nil {
		t.Fatal(err)
	}
	names, err = store.TargetNames(context.Background(), []uint{1})
	if err != nil || names[1] != "新群名称" {
		t.Fatalf("stale names: %v %v", names, err)
	}
	if err := db.Migrator().DropTable(&gd.Target{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TargetNames(context.Background(), []uint{1}); err == nil {
		t.Fatal("hidden database error")
	}
}

// setup 为 t 创建单连接 SQLite 和既有商品/SKU/群，返回仓储、连接及固定时钟。
func setup(t *testing.T) (*s.Store, *gorm.DB, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "membership.db")), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&category.Category{}, &p.Product{}, &p.ProductSKU{}, &gd.Target{}, &d.ProductShareCode{}, &d.ProductConfig{}, &d.ProductTarget{}, &d.SKUTerm{}, &d.OrderItemSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&category.Category{ID: 1, Slug: "membership", NameJSON: jsonmap.JSON{"en-US": "Membership"}, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	product := p.Product{ID: 1, CategoryID: 1, IsActive: true, Slug: "membership", TitleJSON: jsonmap.JSON{"zh-CN": "会员"}}
	if err := db.Omit("Category", "SKUs").Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	for index, code := range []string{p.DefaultSKUCode, "MONTH5", "PERMANENT"} {
		if err := db.Create(&p.ProductSKU{ID: uint(index + 1), ProductID: 1, SKUCode: code}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for id := uint(1); id <= 2; id++ {
		target := gd.Target{ID: id, Name: "member", Purpose: "membership", Enabled: true, Version: 1, RouteVersion: 1, BotInstanceID: 9,
			TelegramChatID: -1000 - int64(id), OccupancySlot: fmt.Sprintf("chat:%d", id), Observation: gd.Observation{RouteVersion: 1,
				ChatType: "supergroup", MemberStatus: "administrator", IsMember: true, CanInviteUsers: true, CanRestrictMembers: true,
				State: "ready", ObservedAt: &now}}
		if err := db.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s.New(db), db, now
}

// input 返回包含 DEFAULT、任意月份及永久的完整替换配置，供测试独立修改。
func input() c.ConfigInput {
	return c.ConfigInput{ProductID: 1, Quantity: 1, TargetIDs: []uint{2, 1}, Terms: []d.SKUTerm{
		{SKUID: 1, Kind: d.TermMonths, Months: 2}, {SKUID: 2, Kind: d.TermMonths, Months: 5}, {SKUID: 3, Kind: d.TermPermanent}}}
}

// TestConfigCASAndDefault 验证首次创建、默认 SKU、自由月份、永久及过期 CAS。
func TestConfigCASAndDefault(t *testing.T) {
	store, _, now := setup(t)
	ctx := context.Background()
	config, err := store.SaveConfig(ctx, input(), now)
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 1 || config.Enabled || config.Quantity != 1 || config.Terms[0].SKUID != 1 || config.Terms[0].Months != 2 || config.Terms[2].Kind != d.TermPermanent {
		t.Fatalf("unexpected configuration: %+v", config)
	}
	if _, err := store.SaveConfig(ctx, input(), now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("create conflict: %v", err)
	}
	changed := input()
	changed.ExpectedVersion = 1
	changed.Terms[0].Months = 18
	updated, err := store.SaveConfig(ctx, changed, now)
	if err != nil || updated.Version != 2 || updated.Terms[0].Months != 18 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := store.SaveConfig(ctx, changed, now); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	actual, err := store.Config(ctx, 1)
	if err != nil || !reflect.DeepEqual(updated, actual) {
		t.Fatalf("read: %+v %v", actual, err)
	}
	if err := a.New(store).CheckSale(); !errors.Is(err, c.ErrSalesClosed) {
		t.Fatal(err)
	}
}

// TestRejectInvalidConfig 验证整数月份、固定数量、重复项及启用门控，失败不留下配置。
func TestRejectInvalidConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*c.ConfigInput)
	}{
		{"zero", func(in *c.ConfigInput) { in.Terms[0].Months = 0 }},
		{"negative", func(in *c.ConfigInput) { in.Terms[0].Months = -1 }},
		{"overflow", func(in *c.ConfigInput) { in.Terms[0].Months = d.MaxMonths + 1 }},
		{"permanent_months", func(in *c.ConfigInput) { in.Terms[2].Months = 999 }},
		{"unknown_kind", func(in *c.ConfigInput) { in.Terms[0].Kind = "days" }},
		{"quantity", func(in *c.ConfigInput) { in.Quantity = 2 }},
		{"duplicate_group", func(in *c.ConfigInput) { in.TargetIDs = []uint{1, 1} }},
		{"duplicate_sku", func(in *c.ConfigInput) { in.Terms[1].SKUID = 1 }},
		{"foreign_sku", func(in *c.ConfigInput) { in.Terms[0].SKUID = 99 }},
		{"enabled", func(in *c.ConfigInput) { in.Enabled = true }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, _, now := setup(t)
			in := input()
			test.mutate(&in)
			if _, err := store.SaveConfig(context.Background(), in, now); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if _, err := store.Config(context.Background(), 1); !errors.Is(err, c.ErrNotFound) {
				t.Fatalf("partial configuration: %v", err)
			}
		})
	}
}

// TestTargetsCurrentCapability 验证同 Bot、用途、启用、路由和新鲜权限均不可绕过。
func TestTargetsCurrentCapability(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"other_bot", map[string]any{"bot_instance_id": 10}},
		{"disabled", map[string]any{"enabled": false}},
		{"support", map[string]any{"purpose": "support"}},
		{"basic_group", map[string]any{"obs_chat_type": "group"}},
		{"route_changed", map[string]any{"route_version": 2}},
		{"cannot_invite", map[string]any{"obs_can_invite_users": false}},
		{"cannot_restrict", map[string]any{"obs_can_restrict_members": false}},
		{"not_admin", map[string]any{"obs_member_status": "member"}},
		{"left", map[string]any{"obs_is_member": false}},
		{"failed", map[string]any{"obs_error_class": "timeout"}},
		{"checking", map[string]any{"obs_state": "checking"}},
		{"stale", map[string]any{"obs_observed_at": time.Date(2026, 10, 31, 23, 55, 0, 0, time.UTC)}},
		{"future", map[string]any{"obs_observed_at": time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, db, now := setup(t)
			if err := db.Model(&gd.Target{}).Where("id = ?", 2).Updates(test.fields).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveConfig(context.Background(), input(), now); !errors.Is(err, c.ErrCapability) {
				t.Fatalf("expected capability failure: %v", err)
			}
		})
	}
}

// TestConcurrentCAS 验证两个持有相同版本的写入仅一个成功，不进行静默重试覆盖。
func TestConcurrentCAS(t *testing.T) {
	store, _, now := setup(t)
	ctx := context.Background()
	if _, err := store.SaveConfig(ctx, input(), now); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, months := range []int64{5, 18} {
		workers.Add(1)
		go func(months int64) {
			defer workers.Done()
			in := input()
			in.ExpectedVersion = 1
			in.Terms[0].Months = months
			_, err := store.SaveConfig(ctx, in, now)
			results <- err
		}(months)
	}
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, c.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

// TestFailedReplacementRollsBack 验证 CAS 已执行后 SKU 失败仍恢复根版本及原集合。
func TestFailedReplacementRollsBack(t *testing.T) {
	store, _, now := setup(t)
	ctx := context.Background()
	original, err := store.SaveConfig(ctx, input(), now)
	if err != nil {
		t.Fatal(err)
	}
	in := input()
	in.ExpectedVersion = 1
	in.Terms[0].SKUID = 999
	if _, err := store.SaveConfig(ctx, in, now); err == nil {
		t.Fatal("missing SKU accepted")
	}
	actual, err := store.Config(ctx, 1)
	if err != nil || !reflect.DeepEqual(original, actual) {
		t.Fatalf("rollback failed: %+v %v", actual, err)
	}
}

// TestConfigBatchesSKUChecksAndChildren 验证多 SKU 校验和子表写入各仅一次批量操作。
// 使用 setup 的单连接数据库，断言原始输入不被改写且配置语义不变。
func TestConfigBatchesSKUChecksAndChildren(t *testing.T) {
	store, db, now := setup(t)
	queries, targets, terms := 0, 0, 0
	if err := db.Callback().Query().Before("gorm:query").Register("test:sku_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "product_skus" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("test:children_inserts", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case (d.ProductTarget{}).TableName():
			targets++
		case (d.SKUTerm{}).TableName():
			terms++
		}
	}); err != nil {
		t.Fatal(err)
	}
	original := input()
	saved, err := store.SaveConfig(context.Background(), original, now)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || targets != 1 || terms != 1 {
		t.Fatalf("expected batched operations, queries=%d targets=%d terms=%d", queries, targets, terms)
	}
	if len(saved.Terms) != 3 || len(saved.Targets) != 2 || original.Terms[0].ProductID != 0 || original.TargetIDs[0] != 2 {
		t.Fatal("batch changed configuration or mutated caller input")
	}
}

// TestConfigBatchRejectsUnavailableSKU 验证跨商品及软删除 SKU 不能混入批量验证。
// 每个失败事务均不得留下配置根或关联子记录。
func TestConfigBatchRejectsUnavailableSKU(t *testing.T) {
	for _, scenario := range []string{"foreign", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			store, db, now := setup(t)
			fields := map[string]interface{}{"product_id": 2}
			if scenario == "deleted" {
				fields = map[string]interface{}{"deleted_at": now}
			}
			if err := db.Model(&p.ProductSKU{}).Where("id = ?", 2).Updates(fields).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveConfig(context.Background(), input(), now); !errors.Is(err, c.ErrNotFound) {
				t.Fatalf("expected unavailable SKU, got %v", err)
			}
			var count int64
			if err := db.Model(&d.ProductConfig{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("failed batch persisted root: count=%d error=%v", count, err)
			}
		})
	}
}

// TestConfigBatchBoundary 验证跨过单批上限后仍完整保存全部 SKU 期限。
func TestConfigBatchBoundary(t *testing.T) {
	store, db, now := setup(t)
	in := input()
	for index := uint(4); index <= 105; index++ {
		if err := db.Create(&p.ProductSKU{ID: index, ProductID: 1, SKUCode: fmt.Sprintf("BATCH-%d", index)}).Error; err != nil {
			t.Fatal(err)
		}
		in.Terms = append(in.Terms, d.SKUTerm{SKUID: index, Kind: d.TermMonths, Months: 1})
	}
	saved, err := store.SaveConfig(context.Background(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Terms) != 105 || saved.Terms[104].SKUID != 105 {
		t.Fatalf("incomplete batched config: %+v", saved)
	}
}

// TestConfigBatchWriteFailureRollsBack 验证期限批量写失败时，旧配置及关联关系完整保留。
func TestConfigBatchWriteFailureRollsBack(t *testing.T) {
	store, db, now := setup(t)
	if _, err := store.SaveConfig(context.Background(), input(), now); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected term insert failure")
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_terms", func(tx *gorm.DB) {
		if tx.Statement.Table == (d.SKUTerm{}).TableName() {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	changed := input()
	changed.ExpectedVersion = 1
	changed.TargetIDs = []uint{1}
	changed.Terms[0].Months = 18
	if _, err := store.SaveConfig(context.Background(), changed, now); !errors.Is(err, failure) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	actual, err := store.Config(context.Background(), 1)
	if err != nil || actual.Version != 1 || len(actual.Targets) != 2 || actual.Terms[0].Months != 2 {
		t.Fatalf("partial batch persisted: config=%+v error=%v", actual, err)
	}
}
