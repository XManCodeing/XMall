//go:build integration

package integrationtest

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/homles698698-lab/XMall/internal/authz"
	"github.com/homles698698-lab/XMall/internal/bootstrap/database/migrations"
	adminapp "github.com/homles698698-lab/XMall/internal/modules/identity/admin/application"
	admindomain "github.com/homles698698-lab/XMall/internal/modules/identity/admin/domain"
	adminstore "github.com/homles698698-lab/XMall/internal/modules/identity/admin/infrastructure/gormstore"
	userdomain "github.com/homles698698-lab/XMall/internal/modules/identity/user/domain"
	resellerdomain "github.com/homles698698-lab/XMall/internal/modules/reseller/domain"
	"github.com/homles698698-lab/XMall/internal/persistence/gormutil"
	"github.com/homles698698-lab/XMall/internal/platform/database/gormdb"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TEST_MYSQL_DSN is a server login for an isolated test instance, not a live
// application DSN. Only a newly generated database is migrated and dropped.
// No existing database named in the DSN is accessed.
func setupMySQLIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is unset; real MySQL integration not executed")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test MySQL DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	server, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal("cannot connect to isolated MySQL test server")
	}
	serverSQL, _ := server.DB()
	t.Cleanup(func() { _ = serverSQL.Close() })
	var version string
	if err := server.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("MySQL integration server: %s", version)
	if !strings.HasPrefix(version, "8.") || strings.Contains(strings.ToLower(version), "mariadb") {
		t.Fatal("this compatibility suite requires MySQL 8.x")
	}
	name := fmt.Sprintf("xmall_mysql_test_%d", time.Now().UnixNano())
	if err := server.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("drop isolated database: %v", err)
		}
	})
	cfg.DBName = name
	previous := gormdb.DB
	if err := gormdb.InitDB("mysql", cfg.FormatDSN(), gormdb.DBPoolConfig{MaxOpenConns: 8, MaxIdleConns: 2}, "release"); err != nil {
		t.Fatal(err)
	}
	db := gormdb.DB
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close(); gormdb.DB = previous })
	return db
}

func TestMySQLFullInitializationAndRestart(t *testing.T) {
	db := setupMySQLIntegrationDB(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrations.AutoMigrate(); err != nil {
			t.Fatalf("full migration run %d: %v", attempt+1, err)
		}
	}
	var jsonText string
	if err := db.Raw("SELECT "+gormutil.JSONTextExprByDialect("mysql", "?", "en-US"), `{"en-US":"Mixed Case title"}`, `{"en-US":"Mixed Case title"}`).Scan(&jsonText).Error; err != nil {
		t.Fatal(err)
	}
	if jsonText != "Mixed Case title" {
		t.Fatalf("JSON text is quoted or corrupted: %q", jsonText)
	}
	var arrayLength int
	if err := db.Raw("SELECT "+gormutil.JSONArrayLengthExprByDialect("mysql", "?"), `[1,2]`).Scan(&arrayLength).Error; err != nil {
		t.Fatal(err)
	}
	if arrayLength != 2 {
		t.Fatalf("JSON length %d", arrayLength)
	}

	store := adminstore.New(db)
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", ""); err != nil {
		t.Fatal(err)
	}
	var adminCount int64
	if err := db.Model(&admindomain.Admin{}).Count(&adminCount).Error; err != nil {
		t.Fatal(err)
	}
	if adminCount != 0 {
		t.Fatal("administrator created without an explicit bootstrap password")
	}
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", "Initial-Test-Password-123!"); err != nil {
		t.Fatal(err)
	}
	var first admindomain.Admin
	if err := db.Where("username = ?", "mysql-admin").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if !first.IsSuper || bcrypt.CompareHashAndPassword([]byte(first.PasswordHash), []byte("Initial-Test-Password-123!")) != nil {
		t.Fatal("first administrator was not initialized correctly")
	}
	if err := db.Model(&first).Update("is_super", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := adminapp.InitDefaultAdmin(store, "mysql-admin", "Must-Not-Replace-Password-456!"); err != nil {
		t.Fatal(err)
	}
	var current admindomain.Admin
	if err := db.First(&current, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.IsSuper || current.PasswordHash != first.PasswordHash {
		t.Fatal("restart changed existing administrator")
	}
	service, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.BootstrapBuiltinRoles(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := service.Enforcer().AddPolicy("mysql-test-reader", "/admin/mysql-test", "GET"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := reloaded.Enforce("mysql-test-reader", "/admin/mysql-test", "GET")
	if err != nil || !allowed {
		t.Fatalf("persisted permission not restored: allowed=%v err=%v", allowed, err)
	}
	allowed, err = reloaded.Enforce("mysql-test-reader", "/admin/mysql-test", "DELETE")
	if err != nil || allowed {
		t.Fatalf("unexpected write permission: allowed=%v err=%v", allowed, err)
	}

	// Active-only uniqueness must reject duplicates but allow recreation after deletion.
	user := userdomain.User{Email: "mysql-reseller@example.invalid", PasswordHash: "test-hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	profile := resellerdomain.Profile{UserID: user.ID}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	domain := resellerdomain.Domain{ResellerID: profile.ID, Domain: "mysql-test.example.invalid", Type: "custom"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := resellerdomain.Domain{ResellerID: profile.ID, Domain: domain.Domain, Type: "custom"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("active domain uniqueness was lost")
	}
	if err := db.Model(&domain).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatalf("cannot reuse soft-deleted domain: %v", err)
	}
	if err := db.Model(&duplicate).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	third := resellerdomain.Domain{ResellerID: profile.ID, Domain: domain.Domain, Type: "custom"}
	if err := db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrations.AutoMigrate(); err != nil {
		t.Fatalf("restart with deleted history: %v", err)
	}

	// Validate the dialect's guarded update semantics under real contention.
	// Repository-specific payment/fulfillment tests are separate from this probe.
	if err := db.Exec("CREATE TABLE mysql_transaction_probe (id BIGINT PRIMARY KEY, stock INT NOT NULL, balance DECIMAL(20,2) NOT NULL, event_key VARCHAR(64) UNIQUE) ENGINE=InnoDB").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (1, 1, 10.00, 'PayCaseA')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (2, 0, 0, 'paycasea')").Error; err != nil {
		t.Fatalf("opaque keys lost case sensitivity: %v", err)
	}
	if err := db.Exec("INSERT INTO mysql_transaction_probe (id, stock, balance, event_key) VALUES (3, 0, 0, 'PayCaseA')").Error; err == nil {
		t.Fatal("duplicate event key was accepted")
	}
	var wg sync.WaitGroup
	results := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := db.Exec("UPDATE mysql_transaction_probe SET stock = stock - 1, balance = balance - 2.50 WHERE id = 1 AND stock > 0 AND balance >= 2.50")
			if r.Error != nil {
				t.Errorf("guarded update: %v", r.Error)
			}
			results <- r.RowsAffected
		}()
	}
	wg.Wait()
	close(results)
	var winners int64
	for n := range results {
		winners += n
	}
	if winners != 1 {
		t.Fatalf("expected one stock claim, got %d", winners)
	}
	rollback := errors.New("intentional rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE mysql_transaction_probe SET balance = 0 WHERE id = 1").Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback returned %v", err)
	}
	var balance string
	if err := db.Raw("SELECT balance FROM mysql_transaction_probe WHERE id = 1").Scan(&balance).Error; err != nil {
		t.Fatal(err)
	}
	if balance != "7.50" {
		t.Fatalf("unexpected decimal balance %s", balance)
	}
}
