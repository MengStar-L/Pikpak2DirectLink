package pikpak

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type failingRoundTripper struct {
	err error
}

func (t failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func TestRedactRequestURLErrorRemovesNestedURLs(t *testing.T) {
	outerURL := "https://api.example/request?authorization=outer-secret"
	redirectURL := "https://redirect.example/request?authorization=redirect-secret"
	err := &url.Error{
		Op:  "Get",
		URL: outerURL,
		Err: &url.Error{Op: "Get", URL: redirectURL, Err: errors.New("redirect failed")},
	}

	redacted := redactRequestURLError(err)
	message := redacted.Error()
	if strings.Contains(message, outerURL) || strings.Contains(message, redirectURL) {
		t.Fatalf("redacted error leaked URL: %q", message)
	}
	var requestErr *url.Error
	if !errors.As(redacted, &requestErr) || requestErr.URL != "[redacted PikPak URL]" {
		t.Fatalf("redacted error = %#v", redacted)
	}
	var nested *url.Error
	if !errors.As(requestErr.Err, &nested) || nested.URL != "[redacted PikPak URL]" {
		t.Fatalf("nested redacted error = %#v", requestErr.Err)
	}
}

func TestSendRedactsTransportURLs(t *testing.T) {
	redirectURL := "https://redirect.example/request?authorization=redirect-secret"
	client := NewClient(Config{})
	client.http = &http.Client{Transport: failingRoundTripper{err: &url.Error{
		Op:  "Get",
		URL: redirectURL,
		Err: errors.New("redirect failed"),
	}}}
	req, err := http.NewRequest(http.MethodGet, "https://api.example/request?authorization=request-secret", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	_, _, err = client.send(req)
	if err == nil {
		t.Fatal("send unexpectedly succeeded")
	}
	if message := err.Error(); strings.Contains(message, "request-secret") || strings.Contains(message, "redirect-secret") {
		t.Fatalf("send error leaked URL credentials: %q", message)
	}
}
