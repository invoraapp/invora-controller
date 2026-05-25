package billingclient

import (
	"fmt"
	"sync"
)

// Cache provides thread-safe access to super-admin billing clients keyed by
// InvoraBillingInstance namespace/name.
type Cache struct {
	mu              sync.RWMutex
	instanceClients map[string]*AdminClient
}

// NewCache creates a new empty cache.
func NewCache() *Cache {
	return &Cache{
		instanceClients: make(map[string]*AdminClient),
	}
}

func cacheKey(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// GetOrCreateInstanceAdmin returns a cached super-admin client for the given
// InvoraBillingInstance, creating one if it doesn't exist.
func (c *Cache) GetOrCreateInstanceAdmin(namespace, name string, cfg AdminConfig) (*AdminClient, error) {
	key := cacheKey(namespace, name)

	c.mu.RLock()
	if client, ok := c.instanceClients[key]; ok {
		c.mu.RUnlock()
		return client, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if client, ok := c.instanceClients[key]; ok {
		return client, nil
	}

	client, err := NewAdmin(cfg)
	if err != nil {
		return nil, err
	}
	c.instanceClients[key] = client
	return client, nil
}

// InvalidateInstance removes the super-admin client for the given instance.
func (c *Cache) InvalidateInstance(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.instanceClients, cacheKey(namespace, name))
}

// InvalidateAll removes all cached clients.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instanceClients = make(map[string]*AdminClient)
}
