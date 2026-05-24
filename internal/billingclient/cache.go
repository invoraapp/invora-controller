package billingclient

import (
	"fmt"
	"sync"
)

// Cache provides thread-safe access to billing gateway clients. It maintains two
// separate maps: one for instance-level clients (keyed by InvoraBillingInstance ns/name)
// and one for per-org clients (keyed by InvoraBillingOrganization ns/name).
type Cache struct {
	mu              sync.RWMutex
	instanceClients map[string]*Client
	orgClients      map[string]*Client
}

// NewCache creates a new empty cache.
func NewCache() *Cache {
	return &Cache{
		instanceClients: make(map[string]*Client),
		orgClients:      make(map[string]*Client),
	}
}

func cacheKey(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// GetOrCreateInstanceClient returns a cached super-admin client for the given
// InvoraBillingInstance, creating one if it doesn't exist.
func (c *Cache) GetOrCreateInstanceClient(namespace, name string, cfg Config) (*Client, error) {
	key := cacheKey(namespace, name)

	c.mu.RLock()
	if client, ok := c.instanceClients[key]; ok {
		c.mu.RUnlock()
		return client, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if client, ok := c.instanceClients[key]; ok {
		return client, nil
	}

	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	c.instanceClients[key] = client
	return client, nil
}

// GetOrCreateOrgClient returns a cached org-scoped client for the given
// InvoraBillingOrganization, creating one if it doesn't exist.
func (c *Cache) GetOrCreateOrgClient(namespace, name string, cfg Config) (*Client, error) {
	key := cacheKey(namespace, name)

	c.mu.RLock()
	if client, ok := c.orgClients[key]; ok {
		c.mu.RUnlock()
		return client, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if client, ok := c.orgClients[key]; ok {
		return client, nil
	}

	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	c.orgClients[key] = client
	return client, nil
}

// InvalidateInstance removes the super-admin client for the given instance.
func (c *Cache) InvalidateInstance(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.instanceClients, cacheKey(namespace, name))
}

// InvalidateOrg removes the org-scoped client for the given organization.
func (c *Cache) InvalidateOrg(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.orgClients, cacheKey(namespace, name))
}

// InvalidateAll removes all cached clients.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instanceClients = make(map[string]*Client)
	c.orgClients = make(map[string]*Client)
}
