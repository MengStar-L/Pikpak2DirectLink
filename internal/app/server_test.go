package app

import (
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
