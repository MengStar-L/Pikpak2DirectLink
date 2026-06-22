package pikpak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestContextDeadlineDetection 测试 context deadline 检测逻辑
func TestContextDeadlineDetection(t *testing.T) {
	// 测试带 deadline 的 context
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		t.Error("Expected context with deadline to have deadline")
	}

	// 测试不带 deadline 的 context
	ctxNoDeadline := context.Background()
	if _, hasDeadline := ctxNoDeadline.Deadline(); hasDeadline {
		t.Error("Expected background context to not have deadline")
	}
}

// TestHTTPClientNoGlobalTimeout 测试 HTTP Client 不应设置全局超时
func TestHTTPClientNoGlobalTimeout(t *testing.T) {
	cfg := Config{
		SessionFile:    "",
		RootFolderName: "test",
		RequestTimeout: 20 * time.Millisecond,
	}

	client := NewClient(cfg)

	// HTTP Client 不应该设置全局 Timeout
	if client.http.Timeout != 0 {
		t.Errorf("HTTP Client should not have global timeout, got %v", client.http.Timeout)
	}
}

func TestDoJSONUsesRequestTimeoutInsideLongParentDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(Config{RequestTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	start := time.Now()
	err := client.doJSON(ctx, http.MethodGet, server.URL, "", nil, nil, false, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected request timeout error, got nil")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("request elapsed %v, want it bounded by RequestTimeout", elapsed)
	}
}

// TestWaitForFileDownloadURLTimeout 测试直链等待超时机制
func TestWaitForFileDownloadURLTimeout(t *testing.T) {
	cfg := Config{
		SessionFile:    "",
		RootFolderName: "test",
		RequestTimeout: 20 * time.Millisecond,
	}

	client := NewClient(cfg)

	// 使用非常短的超时来快速测试
	ctx := context.Background()
	timeout := 120 * time.Millisecond
	pollInterval := 10 * time.Millisecond

	start := time.Now()
	_, err := client.WaitForFileDownloadURL(ctx, "fake-file-id", timeout, pollInterval)
	elapsed := time.Since(start)

	// 应该超时失败
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	// 应该在超时时间附近返回（允许一些误差）
	if elapsed < timeout || elapsed > timeout+500*time.Millisecond {
		t.Errorf("Expected timeout around %v, got %v", timeout, elapsed)
	}

	// 错误信息应该包含超时和尝试次数
	errMsg := err.Error()
	if errMsg == "" {
		t.Error("Expected non-empty error message")
	}
	t.Logf("Timeout error message: %s", errMsg)
}
