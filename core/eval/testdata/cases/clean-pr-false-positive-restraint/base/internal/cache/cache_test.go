package cache

import "testing"

func TestCacheSetGet(t *testing.T) {
	c := New(nil)
	c.Set("alpha:1", "one")
	got, ok := c.Get("alpha:1")
	if !ok || got != "one" {
		t.Fatalf("expected one, got %q (ok=%v)", got, ok)
	}
}
