package userhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	resellermodule "github.com/XManCodeing/XMall/internal/modules/reseller/contract"
	"github.com/XManCodeing/XMall/internal/platform/http/response"

	"github.com/gin-gonic/gin"
)

// TestOrderDateBoundariesUseEastEight 验证无偏移日期按东八区解释，带偏移 API 时间保留绝对时刻。
func TestOrderDateBoundariesUseEastEight(t *testing.T) {
	for _, scenario := range []struct {
		raw  string
		end  bool
		want string
	}{
		{"2027-01-01", false, "2026-12-31T16:00:00Z"},
		{"2027-01-01", true, "2027-01-01T15:59:59.999999999Z"},
		{"2027-01-01T00:00:00Z", true, "2027-01-01T00:00:00Z"},
	} {
		actual, err := parseOrderTimeQuery(scenario.raw, scenario.end)
		if err != nil || actual == nil || actual.UTC().Format(time.RFC3339Nano) != scenario.want {
			t.Fatalf("parse %q end=%v: %v %v", scenario.raw, scenario.end, actual, err)
		}
	}
}

type orderServiceStub struct {
	err error
}

func (s orderServiceStub) ListUserOrders(userID uint, input resellermodule.OrderListInput) ([]resellermodule.OrderListItem, int64, error) {
	return nil, 0, s.err
}

func (s orderServiceStub) GetUserOrderDetail(userID uint, orderNo string) (*resellermodule.OrderDetail, error) {
	return nil, s.err
}

func (s orderServiceStub) StatsUserOrders(userID uint, input resellermodule.OrderListInput) (resellermodule.OrderStats, error) {
	return resellermodule.OrderStats{}, s.err
}

func TestUserOrderHandlerMapsOrderNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewUserOrderHandler(orderServiceStub{err: resellermodule.ErrOrderNotFound})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/reseller/orders/missing", nil)
	c.Set("user_id", uint(9))
	c.Params = gin.Params{{Key: "order_no", Value: "missing"}}

	h.GetOrderDetail(c)

	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.StatusCode != response.CodeNotFound {
		t.Fatalf("expected not found, body=%s", recorder.Body.String())
	}
}

func TestUserOrderHandlerMapsProfileInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewUserOrderHandler(orderServiceStub{err: resellermodule.ErrProfileInactive})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/reseller/orders", nil)
	c.Set("user_id", uint(9))

	h.ListOrders(c)

	var resp struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.StatusCode != response.CodeBadRequest {
		t.Fatalf("expected bad request, body=%s", recorder.Body.String())
	}
}
