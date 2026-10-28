package broadcasthttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	domain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type deliveryServiceStub struct {
	AdminService
	id, delivery uint
	page, size   int
	err          error
	ctx          context.Context
}

func (s *deliveryServiceStub) ListBroadcasts(ctx context.Context, _ app.ListInput) ([]domain.Broadcast, int64, error) {
	s.ctx = ctx
	return nil, 0, ctx.Err()
}
func (s *deliveryServiceStub) GetBroadcast(ctx context.Context, id uint) (*domain.Broadcast, error) {
	s.ctx = ctx
	return &domain.Broadcast{ID: id}, ctx.Err()
}

func TestBroadcastReadsPreserveRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/api/v1/admin/telegram-bot/broadcasts", "/api/v1/admin/telegram-bot/broadcasts/4"} {
		t.Run(path, func(t *testing.T) {
			s := &deliveryServiceStub{}
			r := gin.New()
			RegisterAdminRoutes(r.Group("/api/v1/admin"), NewAdminHandler(s))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil).WithContext(ctx))
			if s.ctx != ctx {
				t.Fatal("request context was replaced or omitted")
			}
		})
	}
}

func (s *deliveryServiceStub) ListBroadcastDeliveries(_ context.Context, id uint, page, size int) ([]app.DeliveryItem, int64, error) {
	s.id, s.page, s.size = id, page, size
	return []app.DeliveryItem{{ID: 8, BotID: 7, BroadcastID: id, UserID: 3, TelegramUserID: 123, Status: "failed", LastError: "telegram rejected message", Retryable: true}}, 1, s.err
}
func (s *deliveryServiceStub) RetryBroadcastDelivery(_ context.Context, id, delivery uint) error {
	s.id, s.delivery = id, delivery
	return s.err
}
func TestDeliveryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &deliveryServiceStub{}
	r := gin.New()
	RegisterAdminRoutes(r.Group("/api/v1/admin"), NewAdminHandler(s))
	for _, tc := range []struct {
		method, path string
		err          error
		code         int
	}{
		{"GET", "/api/v1/admin/telegram-bot/broadcasts/4/deliveries?page=2&page_size=999&bot_id=999", nil, 0},
		{"POST", "/api/v1/admin/telegram-bot/broadcasts/4/deliveries/8/retry", nil, 0},
		{"POST", "/api/v1/admin/telegram-bot/broadcasts/4/deliveries/8/retry", app.ErrRetryDenied, response.CodeBadRequest},
		{"GET", "/api/v1/admin/telegram-bot/broadcasts/4/deliveries", app.ErrNotFound, response.CodeNotFound},
		{"GET", "/api/v1/admin/telegram-bot/broadcasts/0/deliveries", nil, response.CodeBadRequest},
		{"POST", "/api/v1/admin/telegram-bot/broadcasts/4/deliveries/0/retry", nil, response.CodeBadRequest},
		{"POST", "/api/v1/admin/telegram-bot/broadcasts/no/deliveries/8/retry", nil, response.CodeBadRequest},
	} {
		s.err = tc.err
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		var body struct {
			StatusCode int                 `json:"status_code"`
			Data       json.RawMessage     `json:"data"`
			Pagination response.Pagination `json:"pagination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || body.StatusCode != tc.code {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if tc.code == 0 && tc.method == "GET" {
			if s.id != 4 || s.page != 2 || s.size != 200 || body.Pagination.PageSize != 200 {
				t.Fatal(s, body.Pagination)
			}
			var rows []map[string]interface{}
			if err := json.Unmarshal(body.Data, &rows); err != nil {
				t.Fatal(err)
			}
			if rows[0]["retryable"] != true || rows[0]["user_id"] != float64(3) {
				t.Fatal(rows)
			}
			for _, key := range []string{"payload_cipher", "identity_stamp", "claim", "key"} {
				if _, ok := rows[0][key]; ok {
					t.Fatal("secret exposed", key)
				}
			}
		}
		if tc.code == 0 && tc.method == "POST" {
			var accepted map[string]interface{}
			_ = json.Unmarshal(body.Data, &accepted)
			if accepted["accepted"] != true || s.id != 4 || s.delivery != 8 {
				t.Fatal(accepted)
			}
		}
	}
}
