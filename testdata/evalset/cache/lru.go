// Package cache implements a small in-memory least-recently-used cache.
package cache

import "container/list"

// LRU is a fixed-capacity cache that evicts the least recently used entry
// when it's full. It is not safe for concurrent use.
type LRU struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List // front = most recently used, back = least
}

type entry struct {
	key   string
	value any
}

// New returns an LRU cache holding at most capacity entries.
func New(capacity int) *LRU {
	if capacity < 1 {
		capacity = 1
	}
	return &LRU{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get returns the value stored for key, marking it as recently used.
func (c *LRU) Get(key string) (any, bool) {
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry).value, true
}

// Put stores value under key, evicting the least recently used entry if the
// cache is already at capacity and key is new.
func (c *LRU) Put(key string, value any) {
	if el, ok := c.items[key]; ok {
		el.Value.(*entry).value = value
		c.order.MoveToFront(el)
		return
	}

	if c.order.Len() >= c.capacity {
		c.evictOldest()
	}

	el := c.order.PushFront(&entry{key: key, value: value})
	c.items[key] = el
}

// evictOldest removes the least recently used entry.
func (c *LRU) evictOldest() {
	oldest := c.order.Back()
	if oldest == nil {
		return
	}
	c.order.Remove(oldest)
	delete(c.items, oldest.Value.(*entry).key)
}

// Len returns the number of entries currently cached.
func (c *LRU) Len() int {
	return c.order.Len()
}
