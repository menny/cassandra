package cache

import "sync"

// EvictCallback is invoked when an entry is removed from the cache.
type EvictCallback func(key, value string)

// Cache is a thread-safe in-memory string cache.
type Cache struct {
	mu      sync.RWMutex
	items   map[string]string
	onEvict EvictCallback
}

// New creates a new Cache with an optional eviction callback.
func New(onEvict EvictCallback) *Cache {
	return &Cache{
		items:   make(map[string]string),
		onEvict: onEvict,
	}
}

// Set inserts or updates a key-value pair.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
}

// Get retrieves a value by key.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[key]
	return v, ok
}
