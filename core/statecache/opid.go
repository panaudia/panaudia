package statecache

import "sync/atomic"

// OpIDCounter is a monotonically increasing counter for assigning operation IDs.
// It is safe for concurrent use.
type OpIDCounter struct {
	next atomic.Uint64
}

// Assign returns the next operation ID. Each call returns a unique,
// strictly increasing value.
func (c *OpIDCounter) Assign() uint64 {
	return c.next.Add(1)
}

// Current returns the most recently assigned operation ID without advancing
// the counter. Returns 0 if no IDs have been assigned yet.
func (c *OpIDCounter) Current() uint64 {
	return c.next.Load()
}
