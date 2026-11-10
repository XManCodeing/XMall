package catalogproductbootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	bootstrap "github.com/XManCodeing/XMall/internal/bootstrap/catalogproduct"
	category "github.com/XManCodeing/XMall/internal/modules/catalog/category/domain"
	p "github.com/XManCodeing/XMall/internal/modules/catalog/product/domain"
	producthttp "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/http"
	presenter "github.com/XManCodeing/XMall/internal/modules/catalog/product/transport/presenter"
	content "github.com/XManCodeing/XMall/internal/modules/content/contract"
	gd "github.com/XManCodeing/XMall/internal/modules/telegram/groups/domain"
	membership "github.com/XManCodeing/XMall/internal/modules/telegram/membership/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/membership/domain"
	ms "github.com/XManCodeing/XMall/internal/modules/telegram/membership/infrastructure/gormstore"
	"github.com/XManCodeing/XMall/internal/shared/jsonmap"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// publicMembershipPosts 隔离无关文章依赖，不替代真实商品查询和 HTTP 呈现。
type publicMembershipPosts struct{}

// ListPostsForProduct 接受 ctx、商品 ID 和限制，返回空文章集合。
func (publicMembershipPosts) ListPostsForProduct(context.Context, uint, int) ([]content.RelatedPost, error) {
	return nil, nil
}

// publicMembershipRouter 使用真实 bootstrap 查询服务和 ConfigReader 注册公开路由。
func publicMembershipRouter(services bootstrap.Services, reader membership.DisplayReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := bootstrap.NewPublicHTTP(bootstrap.PublicHTTPDependencies{
		Products: services.Read, Membership: reader, RelatedPosts: publicMembershipPosts{},
	})
	producthttp.RegisterPublicRoutes(router.Group("/api/v1/public"), handler)
	return router
}

// publicMembershipRequest 对 router 发起 path 的 GET，返回完整真实 HTTP 响应。
func publicMembershipRequest(router *gin.Engine, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/public"+path, nil))
	return response
}

// seedPublicMembership 创建公开展示测试数据；直接种子不代表生产允许上架会员商品。
func seedPublicMembership(t *testing.T, db *gorm.DB, terms []d.SKUTerm) {
	t.Helper()
	for index, name := range []string{"Z group", `<b>A & group</b>`} {
		if err := db.Model(&gd.Target{}).Where("id = ?", index+1).Update("name", name).Error; err != nil {
			t.Fatal(err)
		}
	}
	models := []interface{}{
		&category.Category{ID: 1, NameJSON: jsonmap.JSON{"en-US": "Visible"}, Slug: "visible", IsActive: true},
		&p.Product{ID: 1, CategoryID: 1, TitleJSON: jsonmap.JSON{"en-US": "Product"}, Slug: "membership", IsActive: true, PurchaseType: "member", FulfillmentType: "manual"},
		&p.Product{ID: 2, CategoryID: 1, TitleJSON: jsonmap.JSON{"en-US": "Product"}, Slug: "ordinary-member", IsActive: true, PurchaseType: "member", FulfillmentType: "manual"},
		&p.Product{ID: 3, CategoryID: 1, TitleJSON: jsonmap.JSON{"en-US": "Product"}, Slug: "hidden-membership", IsActive: false, PurchaseType: "member", FulfillmentType: "manual"},
		&d.ProductConfig{ProductID: 1, Version: 7, Quantity: 1, Terms: terms, Targets: []d.ProductTarget{{ProductID: 1, TargetID: 2}, {ProductID: 1, TargetID: 1}}},
		&d.ProductConfig{ProductID: 3, Version: 1, Quantity: 1},
	}
	for _, model := range models {
		if err := db.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index, term := range terms {
		code := "DEFAULT"
		if len(terms) > 1 {
			code = []string{"M2", "M5", "M18", "FOREVER"}[index]
		}
		if err := db.Create(&p.ProductSKU{ID: term.SKUID, ProductID: 1, SKUCode: code, IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestPublicMembershipSalesProjection verifies the public flag follows persisted enabled state.
// t owns temporary rows; state changes do not contact Telegram or payment gateways.
func TestPublicMembershipSalesProjection(t *testing.T) {
	services, db := adminSetup(t)
	seedPublicMembership(t, db, []d.SKUTerm{{ProductID: 1, SKUID: 11, Kind: "months", Months: 2}})
	router := publicMembershipRouter(services, ms.New(db))
	for _, enabled := range []bool{true, false} {
		if err := db.Model(&d.ProductConfig{}).Where("product_id = 1").Update("enabled", enabled).Error; err != nil {
			t.Fatal(err)
		}
		response := publicMembershipRequest(router, "/products/membership")
		var envelope struct {
			Data struct {
				Membership *presenter.Membership `json:"membership"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Membership == nil || envelope.Data.Membership.SalesOpen != enabled {
			t.Fatalf("sale projection: %s %v", response.Body.String(), err)
		}
	}
}

// TestPublicMembershipProjection 验证真实列表/详情、DEFAULT、任意月数及永久白名单投影。
func TestPublicMembershipProjection(t *testing.T) {
	cases := []struct {
		name  string
		terms []d.SKUTerm
	}{
		{"default", []d.SKUTerm{{ProductID: 1, SKUID: 11, Kind: "months", Months: 2}}},
		{"multiple", []d.SKUTerm{
			{ProductID: 1, SKUID: 11, Kind: "months", Months: 2},
			{ProductID: 1, SKUID: 12, Kind: "months", Months: 5},
			{ProductID: 1, SKUID: 13, Kind: "months", Months: 18},
			{ProductID: 1, SKUID: 14, Kind: "permanent", Months: 0},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			services, db := adminSetup(t)
			seedPublicMembership(t, db, test.terms)
			router := publicMembershipRouter(services, ms.New(db))
			for _, path := range []string{"/products", "/products/membership"} {
				result := publicMembershipRequest(router, path)
				if result.Code != 200 {
					t.Fatalf("%s: %d %s", path, result.Code, result.Body.String())
				}
				var envelope struct {
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(result.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				var items []presenter.Product
				if path == "/products" {
					if err := json.Unmarshal(envelope.Data, &items); err != nil {
						t.Fatal(err)
					}
					if len(items) != 2 {
						t.Fatalf("hidden product exposed: %s", result.Body.String())
					}
				} else {
					var item presenter.Product
					if err := json.Unmarshal(envelope.Data, &item); err != nil {
						t.Fatal(err)
					}
					items = append(items, item)
				}
				for _, item := range items {
					if item.ID == 2 {
						if item.Membership != nil || item.PurchaseType != "member" {
							t.Fatal("ordinary member semantics changed")
						}
						continue
					}
					view := item.Membership
					if view == nil || view.ConfigVersion != 7 || view.Quantity != 1 || view.SalesOpen || len(view.Terms) != len(test.terms) {
						t.Fatalf("projection: %+v", view)
					}
					if strings.Join(view.GroupNames, "|") != "Z group|<b>A & group</b>" {
						t.Fatalf("group names/order: %+v", view.GroupNames)
					}
					for index, term := range view.Terms {
						expected := test.terms[index]
						if term.SKUID != expected.SKUID || term.Kind != expected.Kind || term.Months != expected.Months || term.SKUCode == "" {
							t.Fatalf("term: %+v", term)
						}
						if test.name == "default" && term.SKUCode != "DEFAULT" {
							t.Fatal("DEFAULT mapping lost")
						}
					}
				}
				for _, forbidden := range []string{"target_ids", "targets", "telegram_chat_id", "bot_instance_id", "route_version", "invite", "token", "occupancy_slot", "observation"} {
					if strings.Contains(result.Body.String(), `"`+forbidden+`"`) {
						t.Fatalf("private field leaked: %s", forbidden)
					}
				}
			}
			result := publicMembershipRequest(router, "/products/hidden-membership")
			if !strings.Contains(result.Body.String(), `"status_code":404`) {
				t.Fatalf("hidden detail: %d %s", result.Code, result.Body.String())
			}
			result = publicMembershipRequest(router, "/products/ordinary-member")
			if result.Code != 200 || strings.Contains(result.Body.String(), `"membership"`) {
				t.Fatalf("ordinary contract: %s", result.Body.String())
			}
		})
	}
}

// TestPublicMembershipTargetFailures 验证目标缺失、用途改变和查询失败均不降级成普通商品。
func TestPublicMembershipTargetFailures(t *testing.T) {
	for _, failure := range []string{"missing", "purpose", "empty", "storage"} {
		t.Run(failure, func(t *testing.T) {
			services, db := adminSetup(t)
			seedPublicMembership(t, db, []d.SKUTerm{{ProductID: 1, SKUID: 11, Kind: "months", Months: 2}})
			var err error
			switch failure {
			case "missing":
				err = db.Delete(&gd.Target{}, 2).Error
			case "purpose":
				err = db.Model(&gd.Target{}).Where("id = ?", 2).Update("purpose", "support").Error
			case "empty":
				err = db.Model(&gd.Target{}).Where("id = ?", 2).Update("name", " ").Error
			case "storage":
				err = db.Migrator().DropTable(&gd.Target{})
			}
			if err != nil {
				t.Fatal(err)
			}
			router := publicMembershipRouter(services, ms.New(db))
			for _, path := range []string{"/products", "/products/membership"} {
				result := publicMembershipRequest(router, path)
				if !strings.Contains(result.Body.String(), `"status_code":500`) || strings.Contains(result.Body.String(), `"group_names"`) {
					t.Fatalf("target failure exposed partial data: %s", result.Body.String())
				}
			}
			result := publicMembershipRequest(router, "/products/ordinary-member")
			if result.Code != 200 || strings.Contains(result.Body.String(), `"membership"`) {
				t.Fatalf("ordinary product changed: %s", result.Body.String())
			}
		})
	}
}

// failedPublicMembershipReader 模拟共享配置不可读，并保留调用次数验证可见性优先。
type failedPublicMembershipReader struct{ calls int }

// Config 记录商品查询，返回存储错误；不允许 HTTP 将其误判为普通商品。
func (reader *failedPublicMembershipReader) Display(context.Context, uint) (*membership.DisplayConfig, error) {
	reader.calls++
	return nil, errors.New("configuration unavailable")
}

// TestPublicMembershipFailClosed 验证列表/详情读取失败拒绝响应，且下架商品先被拒绝。
func TestPublicMembershipFailClosed(t *testing.T) {
	services, db := adminSetup(t)
	seedPublicMembership(t, db, []d.SKUTerm{{ProductID: 1, SKUID: 11, Kind: "months", Months: 2}})
	reader := &failedPublicMembershipReader{}
	router := publicMembershipRouter(services, reader)
	result := publicMembershipRequest(router, "/products/hidden-membership")
	if !strings.Contains(result.Body.String(), `"status_code":404`) || reader.calls != 0 {
		t.Fatalf("visibility bypass: %d / %d", result.Code, reader.calls)
	}
	for _, path := range []string{"/products", "/products/membership", "/products/ordinary-member"} {
		result = publicMembershipRequest(router, path)
		if !strings.Contains(result.Body.String(), `"status_code":500`) {
			t.Fatalf("must fail closed: %d %s", result.Code, result.Body.String())
		}
	}
}
