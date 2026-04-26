//go:build integration

package middleware

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// safeBuf — thread-safe обёртка над bytes.Buffer для перехвата логов из
// горутины NATS-подписки.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestIntegration_RecoverLogsRequestIDOnPanic(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	conn, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer conn.Close()

	var logBuf safeBuf
	log := zerolog.New(&logBuf)

	sub, err := conn.QueueSubscribe("test.panic", "test", Recover(log, func(msg *nats.Msg) {
		panic("boom")
	}))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	req := nats.NewMsg("test.panic")
	req.Header.Set("X-Request-Id", "abc-123")
	req.Data = []byte(`{}`)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := conn.RequestMsgWithContext(ctx, req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	if got := resp.Header.Get("Status"); got != "500" {
		t.Errorf("Status header = %q, want 500", got)
	}
	if string(resp.Data) != `{"error":"internal server error"}` {
		t.Errorf("body = %q, want internal server error JSON", string(resp.Data))
	}

	logStr := logBuf.String()
	if !strings.Contains(logStr, `"req":"abc-123"`) {
		t.Errorf("log missing request ID, got: %s", logStr)
	}
	if !strings.Contains(logStr, `"panic":"boom"`) {
		t.Errorf("log missing panic value, got: %s", logStr)
	}
	if !strings.Contains(logStr, `"subject":"test.panic"`) {
		t.Errorf("log missing subject, got: %s", logStr)
	}
}

// Pub/Sub case: msg.Reply пустой — middleware только логирует, не отвечает.
// Проверяет, что отсутствие Reply не приводит к ошибке/повторному паник.
func TestIntegration_RecoverPanicWithoutReply(t *testing.T) {
	srv := natsserver.RunRandClientPortServer()
	defer srv.Shutdown()

	conn, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer conn.Close()

	var logBuf safeBuf
	log := zerolog.New(&logBuf)

	// done закрывается ПОСЛЕ возврата из Recover'а: defer recover() в Recover
	// успевает залогировать паника до того, как wrapped() возвращает управление
	// и close(done) разблокирует тест.
	done := make(chan struct{})
	inner := Recover(log, func(msg *nats.Msg) {
		panic("boom-noreply")
	})
	wrapped := func(msg *nats.Msg) {
		inner(msg)
		close(done)
	}

	sub, err := conn.QueueSubscribe("test.panic.noreply", "test", wrapped)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if err := conn.Publish("test.panic.noreply", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler did not execute within 2s")
	}

	logStr := logBuf.String()
	if !strings.Contains(logStr, `"panic":"boom-noreply"`) {
		t.Errorf("log missing panic value, got: %s", logStr)
	}
}
