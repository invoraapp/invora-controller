package gateway

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Target converts a gateway URL into a gRPC dial target plus the matching
// transport-credentials option.
//
// gRPC targets are NOT URLs. grpc.NewClient expects a name in "host:port" form
// (the default "dns" resolver's syntax). The scheme position is reserved for a
// *resolver* scheme (dns, passthrough, unix, ...) — not "http"/"https". Passing
// a URL like "https://dev-gateway.invora.app" makes gRPC treat the whole string
// as an address the dns resolver cannot resolve, so the channel never leaves
// CONNECTING and every call fails with "context deadline exceeded". The URL
// scheme here only selects transport security and the default port; it must be
// translated, never forwarded to grpc.NewClient verbatim.
//
//	https://host           -> host:443  + TLS (system roots)
//	https://host:8443      -> host:8443 + TLS
//	http://host            -> host:80   + insecure
//	host:port (no scheme)  -> host:port + insecure (passthrough)
func Target(gatewayURL string) (target string, creds grpc.DialOption, err error) {
	// No scheme: already a bare host[:port] target — pass through as plaintext.
	if !strings.Contains(gatewayURL, "://") {
		return gatewayURL, grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	}

	u, err := url.Parse(gatewayURL)
	if err != nil {
		return "", nil, fmt.Errorf("invalid gateway URL %q: %w", gatewayURL, err)
	}

	host := u.Host
	switch u.Scheme {
	case "https":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "443")
		}
		// serverNameOverride "" -> the TLS ServerName is derived from the dial
		// host, which is what we want for a public gateway hostname.
		return host, grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, "")), nil
	case "http":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
		return host, grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	default:
		return "", nil, fmt.Errorf("unsupported gateway URL scheme %q in %q (want http or https)", u.Scheme, gatewayURL)
	}
}

// Dial opens a gRPC client connection to the gateway URL using the correct
// target form and transport credentials. The connection is lazy
// (grpc.NewClient semantics): callers needing readiness must call
// conn.Connect() and wait on the channel state.
func Dial(gatewayURL string) (*grpc.ClientConn, error) {
	target, creds, err := Target(gatewayURL)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(target, creds)
}
