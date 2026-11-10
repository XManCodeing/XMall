package broadcasthttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	app "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/application"
	domain "github.com/XManCodeing/XMall/internal/modules/telegram/broadcast/domain"
	"github.com/XManCodeing/XMall/internal/platform/http/response"
	"github.com/gin-gonic/gin"
)

type previewServiceStub struct {
	AdminService
	preview, created app.CreateInput
	count            int
	ctx              context.Context
}

func (s *previewServiceStub) PreviewBroadcast(ctx context.Context, input app.CreateInput) (app.PreviewResult, error) {
	s.ctx, s.preview = ctx, input
	return app.PreviewResult{RecipientCount: s.count}, nil
}
func (s *previewServiceStub) CreateBroadcast(ctx context.Context, input app.CreateInput) (*domain.Broadcast, error) {
	s.ctx, s.created = ctx, input
	return &domain.Broadcast{ID: 1}, nil
}
func TestPreviewRouteUsesCreateContractAndAllowsZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &previewServiceStub{}
	router := gin.New()
	RegisterAdminRoutes(router.Group("/api/v1/admin"), NewAdminHandler(s))
	body := `{"bot_id":7,"title":"test","recipient_type":"specific","user_ids":[1],"filters":{"keyword":"x"},"message_html":"private body","attachment_url":"/uploads/photo.png","attachment_name":"photo.png","buttons":[[{"text":"Shop","url":"https://example.test"}]]}`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, suffix := range []string{"/preview", ""} {
		req := httptest.NewRequest("POST", "/api/v1/admin/telegram-bot/broadcasts"+suffix, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var got struct {
			StatusCode int                        `json:"status_code"`
			Data       map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.StatusCode != 0 || rec.Code != 200 {
			t.Fatalf("%s %v", rec.Body, err)
		}
		if suffix != "" && (len(got.Data) != 1 || string(got.Data["recipient_count"]) != "0") {
			t.Fatalf("wrong preview shape: %s", rec.Body)
		}
		if s.ctx != ctx {
			t.Fatal("lost request context")
		}
	}
	if !reflect.DeepEqual(s.preview, s.created) || len(s.preview.Buttons) != 1 || s.preview.BotID != 7 {
		t.Fatal("preview/create request contract differs")
	}
	for _, body := range []string{`{}`, `{"bot_id":7,"title":"test","recipient_type":"all"}`} {
		req := httptest.NewRequest("POST", "/api/v1/admin/telegram-bot/broadcasts/preview", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var got struct {
			StatusCode int `json:"status_code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.StatusCode == 0 {
			t.Fatal("invalid preview input accepted")
		}
	}
}

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
func (s *deliveryServiceStub) GetBroadcast(ctx context.Context, id uint) (*app.BroadcastDetail, error) {
	s.ctx = ctx
	return &app.BroadcastDetail{Broadcast: domain.Broadcast{ID: id}}, ctx.Err()
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
