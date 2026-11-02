# Cache Review Context

- `Cache.onEvict` is intentionally invoked **outside** of `c.mu` so that user-supplied eviction callbacks can safely call back into `Cache` methods without deadlocking.
- Methods that remove multiple entries (such as batch or prefix eviction) must collect removed key/value pairs while holding `c.mu.Lock()`, release `c.mu.Unlock()`, and then invoke `c.onEvict(k, v)` for each removed entry.
