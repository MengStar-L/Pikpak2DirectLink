package main

import (
	"net/http"
	"testing"
)

func TestNewHTTPServerAppliesConnectionLimits(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer("127.0.0.1:51873", handler)

	if server.Addr != "127.0.0.1:51873" {
		t.Fatalf("Addr = %q, want loopback address", server.Addr)
	}
	if server.Handler == nil {
		t.Fatal("Handler is nil")
	}
	if server.ReadHeaderTimeout != readHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, readHeaderTimeout)
	}
	if server.ReadTimeout != readTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", server.ReadTimeout, readTimeout)
	}
	if server.IdleTimeout != idleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", server.IdleTimeout, idleTimeout)
	}
	if server.MaxHeaderBytes != maxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, maxHeaderBytes)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want no global streaming timeout", server.WriteTimeout)
	}
}
