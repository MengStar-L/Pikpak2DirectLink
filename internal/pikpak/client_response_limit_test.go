package pikpak

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseRoundTripper func(*http.Request) (*http.Response, error)

func (f responseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendRejectsOversizedResponseBodies(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewClient(Config{})
			client.http.Transport = responseRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(bytes.NewReader(make([]byte, maxResponseBytes+1))),
					Header:     make(http.Header),
				}, nil
			})

			req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			body, gotStatus, err := client.send(req)
			if err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB limit") {
				t.Fatalf("send error = %v, want response size limit error", err)
			}
			if body != nil {
				t.Fatalf("send body length = %d, want nil", len(body))
			}
			if gotStatus != status {
				t.Fatalf("send status = %d, want %d", gotStatus, status)
			}
		})
	}
}

func TestSendAcceptsResponseBodyAtLimit(t *testing.T) {
	client := NewClient(Config{})
	client.http.Transport = responseRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxResponseBytes))),
			Header:     make(http.Header),
		}, nil
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	body, status, err := client.send(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(body) != maxResponseBytes {
		t.Fatalf("send body length = %d, want %d", len(body), maxResponseBytes)
	}
	if status != http.StatusOK {
		t.Fatalf("send status = %d, want %d", status, http.StatusOK)
	}
}
