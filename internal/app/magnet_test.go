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
			wantHash: "xt=urn:btih:abc123",
			wantDN:   true,
		},
		{
			name:     "带多余参数的磁链",
			input:    "magnet:?xt=urn:btih:abc123&dn=test&xl=12345&ws=http://example.com",
			wantHash: "xt=urn:btih:abc123",
			wantDN:   true,
		},
		{
			name:     "带 tracker 的磁链",
			input:    "magnet:?xt=urn:btih:abc123&tr=udp://tracker1.com:80&tr=udp://tracker2.com:80",
			wantHash: "xt=urn:btih:abc123",
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

// TestNormalizeMagnetLinkPreservesHashLiteral 守护回归：xt 的 info-hash 必须原样
// 保留 "urn:btih:" 字面量。一旦冒号被百分号编码（%3A），PikPak 就无法把磁链
// 识别为种子，多文件种子会塌缩成单个无效条目。
func TestNormalizeMagnetLinkPreservesHashLiteral(t *testing.T) {
	const hash = "fb4ecf3dcfa276fefa0c1c5f5cd1e0adf035d7ad"
	magnet := "magnet:?xt=urn:btih:" + hash

	got := normalizeMagnetLink(magnet)

	want := "magnet:?xt=urn:btih:" + hash
	if got != want {
		t.Errorf("hash literal was mangled\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "%3A") {
		t.Errorf("info-hash colons must not be percent-encoded, got %q", got)
	}
}

// TestNormalizeMagnetLinkPreservesTracker 守护 tracker 的冒号/斜杠不被编码，
// 否则 tr 地址会失效。
func TestNormalizeMagnetLinkPreservesTracker(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:abc123&tr=udp://tracker.example.com:1337/announce"

	got := normalizeMagnetLink(magnet)

	if !strings.Contains(got, "tr=udp://tracker.example.com:1337/announce") {
		t.Errorf("tracker address was mangled, got %q", got)
	}
}
