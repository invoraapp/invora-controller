package billingclient

// Cache is retained for interface compatibility but no longer holds AdminClient
// instances — all billing-admin calls now go through generated gRPC stubs.
type Cache struct{}

// NewCache creates a new empty cache.
func NewCache() *Cache {
	return &Cache{}
}

// InvalidateInstance is a no-op (retained for interface compatibility).
func (c *Cache) InvalidateInstance(namespace, name string) {}

// InvalidateAll is a no-op (retained for interface compatibility).
func (c *Cache) InvalidateAll() {}
