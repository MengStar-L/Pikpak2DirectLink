package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildContentDispositionIncludesFallbackFilename(t *testing.T) {
	t.Parallel()

	header := buildContentDisposition("视频文件.mp4")
	if !strings.Contains(header, `filename="____.mp4"`) {
		t.Fatalf("expected ASCII fallback filename, got %q", header)
	}
	if !strings.Contains(header, "filename*=UTF-8''%E8%A7%86%E9%A2%91%E6%96%87%E4%BB%B6.mp4") {
		t.Fatalf("expected UTF-8 filename, got %q", header)
	}
}

func TestProxyErrorsDoNotExposeInternalDetails(t *testing.T) {
	t.Parallel()

	const leakedAccount = "alice@example.com"
	s := &Server{
		jobs: newJobStore(10),
		logs: newLogStore(10),
	}
	s.jobs.create(&Job{
		ID:     "job-proxy",
		Status: JobCompleted,
		Result: &JobResult{
			File:       DownloadItem{ID: "file-1", Name: "file.bin"},
			DirectURL:  "http://example.com/\naccount=" + leakedAccount,
			ProxyToken: "tok",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	req.SetPathValue("id", "job-proxy")
	rec := httptest.NewRecorder()

	s.handleProxy(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected bad gateway, got %d with body %s", rec.Code, body)
	}
	if !strings.Contains(body, proxyDownloadFailedError) {
		t.Fatalf("expected generic proxy error, got %s", body)
	}
	if strings.Contains(body, leakedAccount) {
		t.Fatalf("proxy response leaked account detail: %s", body)
	}

	logs := s.logs.list(0)
	if len(logs) != 1 {
		t.Fatalf("expected one internal log entry, got %d", len(logs))
	}
	if len(logs[0].Details) == 0 || !strings.Contains(logs[0].Details[0], leakedAccount) {
		t.Fatalf("expected internal log to retain the diagnostic detail, got %+v", logs[0])
	}
}
