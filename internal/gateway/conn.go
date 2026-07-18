package gateway

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ConnCache manages reusable gRPC connections keyed by gateway URL.
type ConnCache struct {
	mu    sync.RWMutex
	conns map[string]*grpc.ClientConn
}

func NewConnCache() *ConnCache {
	return &ConnCache{conns: make(map[string]*grpc.ClientConn)}
}

// Get returns a cached connection or creates a new one for the given gateway URL.
func (c *ConnCache) Get(gatewayURL string) (*grpc.ClientConn, error) {
	c.mu.RLock()
	if conn, ok := c.conns[gatewayURL]; ok {
		c.mu.RUnlock()
		return conn, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if conn, ok := c.conns[gatewayURL]; ok {
		return conn, nil
	}

	// Dial derives the gRPC target (host:port) + transport creds from the URL
	// scheme. Never pass the raw URL to grpc.NewClient — see gateway.Target.
	conn, err := Dial(gatewayURL)
	if err != nil {
		return nil, fmt.Errorf("dialing gateway %s: %w", gatewayURL, err)
	}
	c.conns[gatewayURL] = conn
	return conn, nil
}

// Invalidate closes and removes the connection for the given URL.
func (c *ConnCache) Invalidate(gatewayURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[gatewayURL]; ok {
		conn.Close()
		delete(c.conns, gatewayURL)
	}
}

// AuthContext returns a context with Bearer token and optional org ID in gRPC metadata.
// The org is asserted via x-zitadel-orgid (gateway-validated); x-invora-org-id is
// a trusted-internal header the gateway strips from inbound requests.
func AuthContext(ctx context.Context, token, orgID string) context.Context {
	pairs := []string{"authorization", "Bearer " + token}
	if orgID != "" {
		pairs = append(pairs, "x-zitadel-orgid", orgID)
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}
