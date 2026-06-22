package app

import (
	"strings"
	"testing"
)

func TestNormalizeMagnetLink(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHash string // 检查是否包含 xt 哈希
		wantDN   bool   // 检查是否保留 dn
	}{
		{
			name:     "标准磁链",
			input:    "magnet:?xt=urn:btih:abc123&dn=test",
			wantHash: "xt=urn%3Abtih%3Aabc123",
			wantDN:   true,
		},
		{
			name:     "带多余参数的磁链",
			input:    "magnet:?xt=urn:btih:abc123&dn=test&xl=12345&ws=http://example.com",
			wantHash: "xt=urn%3Abtih%3Aabc123",
			wantDN:   true,
		},
		{
			name:     "带 tracker 的磁链",
			input:    "magnet:?xt=urn:btih:abc123&tr=udp://tracker1.com:80&tr=udp://tracker2.com:80",
			wantHash: "xt=urn%3Abtih%3Aabc123",
			wantDN:   false,
		},
		{
			name:     "无效磁链返回原值",
			input:    "not a magnet",
			wantHash: "",
			wantDN:   false,
		},
		{
			name:     "空磁链",
			input:    "",
			wantHash: "",
			wantDN:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeMagnetLink(tt.input)

			if !strings.HasPrefix(strings.ToLower(tt.input), "magnet:?") {
				if got != tt.input {
					t.Errorf("invalid magnet should return original, got %q", got)
				}
				return
			}

			if tt.wantHash != "" && !strings.Contains(got, tt.wantHash) {
				t.Errorf("normalized magnet missing hash, got %q", got)
			}

			if tt.wantDN && !strings.Contains(got, "dn=") {
				t.Errorf("normalized magnet missing dn, got %q", got)
			}

			// 清洗后的磁链应该不包含 xl、ws 等多余参数
			if strings.Contains(got, "xl=") || strings.Contains(got, "ws=") {
				t.Errorf("normalized magnet should not contain xl/ws, got %q", got)
			}
		})
	}
}

func TestNormalizeMagnetLinkIdempotent(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:abc123&dn=test&xl=999&ws=http://example.com&tr=udp://tracker.com:80"

	first := normalizeMagnetLink(magnet)
	second := normalizeMagnetLink(first)

	if first != second {
		t.Errorf("normalization should be idempotent, first=%q, second=%q", first, second)
	}
}
