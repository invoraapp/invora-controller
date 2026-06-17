package gateway

import "testing"

func TestTarget(t *testing.T) {
	tests := []struct {
		name       string
		gatewayURL string
		wantTarget string
		wantErr    bool
	}{
		// The regression this guards: a URL must become host:port, never be
		// forwarded to grpc.NewClient with its scheme intact.
		{"https no port", "https://dev-gateway.invora.app", "dev-gateway.invora.app:443", false},
		{"https explicit port", "https://dev-gateway.invora.app:8443", "dev-gateway.invora.app:8443", false},
		{"http no port", "http://gateway.invora-dev.svc.cluster.local", "gateway.invora-dev.svc.cluster.local:80", false},
		{"http explicit port", "http://localhost:8080", "localhost:8080", false},
		{"bare host:port passthrough", "gateway.invora-dev.svc.cluster.local:8081", "gateway.invora-dev.svc.cluster.local:8081", false},
		{"unsupported scheme", "ftp://gateway.invora.app", "", true},
		{"grpc-ish scheme rejected", "dns:///gateway.invora.app:443", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, creds, err := Target(tt.gatewayURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Target(%q): expected error, got target=%q", tt.gatewayURL, target)
				}
				return
			}
			if err != nil {
				t.Fatalf("Target(%q): unexpected error: %v", tt.gatewayURL, err)
			}
			if target != tt.wantTarget {
				t.Errorf("Target(%q): target = %q, want %q", tt.gatewayURL, target, tt.wantTarget)
			}
			if creds == nil {
				t.Errorf("Target(%q): creds DialOption is nil", tt.gatewayURL)
			}
		})
	}
}

func TestDial_RejectsBadScheme(t *testing.T) {
	// A bad scheme must fail fast at Dial (Target error), not silently produce a
	// channel that hangs until "context deadline exceeded".
	if _, err := Dial("ftp://gateway.invora.app"); err == nil {
		t.Fatal("Dial: expected error for unsupported scheme, got nil")
	}
}
