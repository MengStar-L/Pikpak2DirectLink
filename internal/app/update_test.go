package app

import (
	"io"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want int // sign of expected result
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.3.0", "v1.2.9", 1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.2.10", "v1.2.9", 1},
		{"dev", "v1.0.0", -1},
		{"v1.0.0", "dev", 1},
		{"dev", "dev", 0},
		{"", "v1.0.0", -1},
		{"v1.0.0", "v1.0.0-rc1", 1},  // release beats prerelease
		{"v1.0.0-rc1", "v1.0.0", -1}, // prerelease loses to release
		{"v1.0.0-rc2", "v1.0.0-rc1", 1},
		{"v1.0.0+build5", "v1.0.0", 0}, // build metadata ignored
	}

	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		if sign(got) != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}

func TestExpectedAssetName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "Pikpak2DirectLink_linux_amd64"},
		{"linux", "arm64", "Pikpak2DirectLink_linux_arm64"},
		{"darwin", "arm64", "Pikpak2DirectLink_darwin_arm64"},
		{"windows", "amd64", "Pikpak2DirectLink_windows_amd64.exe"},
	}
	for _, tc := range cases {
		if got := expectedAssetName(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("expectedAssetName(%q,%q)=%q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestSelectAsset(t *testing.T) {
	t.Parallel()

	assets := []ghAsset{
		{Name: "SHA256SUMS"},
		{Name: "Pikpak2DirectLink_linux_amd64", URL: "u1", Size: 10},
		{Name: "Pikpak2DirectLink_linux_arm64", URL: "u2", Size: 20},
		{Name: "Pikpak2DirectLink_windows_amd64.exe", URL: "u3", Size: 30},
	}

	got, ok := selectAsset(assets, "linux", "arm64")
	if !ok || got.URL != "u2" {
		t.Fatalf("exact match failed: %+v ok=%v", got, ok)
	}

	got, ok = selectAsset(assets, "windows", "amd64")
	if !ok || got.URL != "u3" {
		t.Fatalf("windows match failed: %+v ok=%v", got, ok)
	}

	if _, ok := selectAsset(assets, "darwin", "amd64"); ok {
		t.Fatalf("expected no match for darwin/amd64")
	}

	// arm (32-bit) must NOT be satisfied by an arm64 asset — the token-boundary
	// fallback must treat "arm" and "arm64" as distinct.
	armOnly64 := []ghAsset{
		{Name: "SHA256SUMS"},
		{Name: "Pikpak2DirectLink_linux_arm64", URL: "u-arm64"},
	}
	if _, ok := selectAsset(armOnly64, "linux", "arm"); ok {
		t.Fatalf("linux/arm must not match an arm64 asset")
	}
	if got, ok := selectAsset(armOnly64, "linux", "arm64"); !ok || got.URL != "u-arm64" {
		t.Fatalf("linux/arm64 exact match failed: %+v ok=%v", got, ok)
	}

	// Fallback token matching tolerates naming drift but skips the checksum file.
	drift := []ghAsset{
		{Name: "SHA256SUMS"},
		{Name: "pikpak2directlink-linux-amd64-v2", URL: "drift"},
	}
	got, ok = selectAsset(drift, "linux", "amd64")
	if !ok || got.URL != "drift" {
		t.Fatalf("fallback match failed: %+v ok=%v", got, ok)
	}
}

func TestLookupChecksum(t *testing.T) {
	t.Parallel()

	manifest := "" +
		"abc123  Pikpak2DirectLink_linux_amd64\n" +
		"def456 *Pikpak2DirectLink_windows_amd64.exe\n" +
		"# a comment line\n" +
		"\n" +
		"ghi789  ./nested/Pikpak2DirectLink_linux_arm64\n"

	if hash, ok := lookupChecksum(manifest, "Pikpak2DirectLink_linux_amd64"); !ok || hash != "abc123" {
		t.Errorf("amd64 lookup = %q ok=%v", hash, ok)
	}
	if hash, ok := lookupChecksum(manifest, "Pikpak2DirectLink_windows_amd64.exe"); !ok || hash != "def456" {
		t.Errorf("windows lookup (binary marker) = %q ok=%v", hash, ok)
	}
	if hash, ok := lookupChecksum(manifest, "Pikpak2DirectLink_linux_arm64"); !ok || hash != "ghi789" {
		t.Errorf("arm64 lookup (path prefix) = %q ok=%v", hash, ok)
	}
	if _, ok := lookupChecksum(manifest, "missing"); ok {
		t.Errorf("expected missing file to fail lookup")
	}
}

func TestProgressReaderNotifies(t *testing.T) {
	t.Parallel()

	// 700KB across reads should trigger at least the threshold + EOF notifies.
	data := make([]byte, 700<<10)
	var lastRead int64
	notifies := 0
	pr := &progressReader{
		reader: bytesChunks(data, 64<<10),
		total:  int64(len(data)),
		onProgress: func(read, total int64) {
			notifies++
			lastRead = read
		},
	}

	buf := make([]byte, 64<<10)
	for {
		_, err := pr.Read(buf)
		if err != nil {
			break
		}
	}

	if notifies == 0 {
		t.Fatal("expected progress notifications")
	}
	if lastRead != int64(len(data)) {
		t.Fatalf("final read = %d, want %d", lastRead, len(data))
	}
}

func TestStartInstallGuards(t *testing.T) {
	t.Parallel()

	u := newUpdater("owner/repo", 0, nil)

	// No update available yet.
	if err := u.startInstall(); err == nil {
		t.Fatal("expected error when no update is available")
	}

	// Pretend a managed update is available, but mark busy: should be rejected.
	u.mutate(func(s *updateStatus) {
		s.Managed = true
		s.UpdateAvailable = true
		s.LatestVersion = "v9.9.9"
	})
	u.busy = true
	if err := u.startInstall(); err == nil {
		t.Fatal("expected error when an operation is in progress")
	}
}

// bytesChunks returns a reader that yields data in fixed-size chunks so the
// progress reader sees multiple reads.
func bytesChunks(data []byte, chunk int) *chunkReader {
	return &chunkReader{data: data, chunk: chunk}
}

type chunkReader struct {
	data  []byte
	chunk int
	pos   int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := c.pos + c.chunk
	if end > len(c.data) {
		end = len(c.data)
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}
