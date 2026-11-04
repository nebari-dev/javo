package ratelimit

import "sync"

// Concurrent counts what each key holds at once, up to a maximum.
type Concurrent struct {
	max int
	mu  sync.Mutex
	n   map[string]int
}

// NewConcurrent allows each key max holders at once.
func NewConcurrent(max int) *Concurrent {
	return &Concurrent{max: max, n: map[string]int{}}
}

// Max is how many holders one key may have.
func (c *Concurrent) Max() int { return c.max }

// Acquire takes a place for key, or reports false when key already holds
// Max. The returned func gives the place back; call it once.
func (c *Concurrent) Acquire(key string) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key] >= c.max {
		return nil, false
	}
	c.n[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.n[key]--; c.n[key] <= 0 {
				delete(c.n, key)
			}
		})
	}, true
}
