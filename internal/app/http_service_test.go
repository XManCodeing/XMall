package app

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServiceStartAndStop(t *testing.T) {
	service := NewHTTPService("127.0.0.1:0", http.NotFoundHandler())
	listening := make(chan net.Listener, 1)
	service.server.BaseContext = func(listener net.Listener) context.Context {
		listening <- listener
		return context.Background()
	}
	done := make(chan error, 1)
	go func() { done <- service.Start(context.Background()) }()
	t.Cleanup(func() { _ = service.server.Close() })
	var listener net.Listener
	select {
	case listener = <-listening:
	case err := <-done:
		t.Fatalf("start failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP service did not start")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected response: %d", response.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("HTTP service did not stop")
	}
}

func TestHTTPServiceReportsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	service := NewHTTPService(listener.Addr().String(), http.NotFoundHandler())
	if err := service.Start(context.Background()); err == nil {
		t.Fatal("occupied port must not report successful startup")
	}
}

func TestNewHTTPServiceConfiguresDefensiveTimeouts(t *testing.T) {
	service := NewHTTPService(":0", http.NotFoundHandler())
	if service.server.ReadHeaderTimeout <= 0 ||
		service.server.ReadTimeout <= 0 ||
		service.server.WriteTimeout <= 0 ||
		service.server.IdleTimeout <= 0 {
		t.Fatalf("HTTP server timeouts must all be configured: %+v", service.server)
	}
	if service.server.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", service.server.MaxHeaderBytes, httpMaxHeaderBytes)
	}
}
