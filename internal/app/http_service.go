package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/homles698698-lab/XMall/internal/logger"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpWriteTimeout      = 60 * time.Second
	httpIdleTimeout       = 120 * time.Second
	httpMaxHeaderBytes    = 1 << 20
)

// HTTPService HTTP 服务封装
type HTTPService struct {
	name   string
	server *http.Server
}

// NewHTTPService 创建 HTTP 服务
func NewHTTPService(addr string, handler http.Handler) *HTTPService {
	return &HTTPService{
		name: "http",
		server: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: httpReadHeaderTimeout,
			ReadTimeout:       httpReadTimeout,
			WriteTimeout:      httpWriteTimeout,
			IdleTimeout:       httpIdleTimeout,
			MaxHeaderBytes:    httpMaxHeaderBytes,
		},
	}
}

// Name 服务名称
func (s *HTTPService) Name() string {
	if s == nil || s.name == "" {
		return "http"
	}
	return s.name
}

// Start 启动服务
func (s *HTTPService) Start(ctx context.Context) error {
	if s == nil || s.server == nil {
		return errors.New("http server not initialized")
	}
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	logger.Infow("http_listening", "addr", listener.Addr().String(), "message", "HTTP 服务已开始监听")
	if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 停止服务
func (s *HTTPService) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
