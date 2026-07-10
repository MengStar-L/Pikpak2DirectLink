package app

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactProxyRequestErrorRemovesRedirectURLStructurally(t *testing.T) {
	sourceURL := "https://cdn.example/source?token=source-secret"
	redirectURL := "https://redirect.example/file?token=redirect-secret"
	err := &url.Error{
		Op:  "Get",
		URL: redirectURL,
		Err: errors.New("stopped after too many redirects"),
	}

	redacted := redactProxyRequestError(err, sourceURL)
	message := redacted.Error()
	if strings.Contains(message, sourceURL) || strings.Contains(message, redirectURL) {
		t.Fatalf("redacted error leaked URL: %q", message)
	}
	var requestErr *url.Error
	if !errors.As(redacted, &requestErr) {
		t.Fatalf("redacted error lost *url.Error structure: %T", redacted)
	}
	if requestErr.URL != "[redacted upstream URL]" {
		t.Fatalf("redacted URL = %q", requestErr.URL)
	}
}
