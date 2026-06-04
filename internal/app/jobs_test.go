package app

import "testing"

func TestDetectResourceKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		kind  ResourceKind
		ok    bool
	}{
		{name: "magnet", input: "magnet:?xt=urn:btih:abc", kind: ResourceMagnet, ok: true},
		{name: "share", input: "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1", kind: ResourceShare, ok: true},
		{name: "invalid", input: "https://example.com/file.zip", ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, err := detectResourceKind(tt.input)
			if tt.ok && err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("expected error, got kind %q", kind)
			}
			if tt.ok && kind != tt.kind {
				t.Fatalf("expected kind %q, got %q", tt.kind, kind)
			}
		})
	}
}

func TestParseShareLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		wantShareID  string
		wantTailID   string
		expectErrors bool
	}{
		{
			name:        "root share",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
		},
		{
			name:        "share with child id",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1/VO8Ba45l-FRcCf559uZjwjFjo1",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantTailID:  "VO8Ba45l-FRcCf559uZjwjFjo1",
		},
		{
			name:         "invalid",
			input:        "https://mypikpak.com/",
			expectErrors: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shareID, tailID, err := parseShareLink(tt.input)
			if tt.expectErrors {
				if err == nil {
					t.Fatalf("expected error, got shareID=%q tailID=%q", shareID, tailID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if shareID != tt.wantShareID {
				t.Fatalf("expected shareID %q, got %q", tt.wantShareID, shareID)
			}
			if tailID != tt.wantTailID {
				t.Fatalf("expected tailID %q, got %q", tt.wantTailID, tailID)
			}
		})
	}
}
