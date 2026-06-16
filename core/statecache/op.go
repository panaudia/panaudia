package statecache

import "time"

// Op represents a single state operation stored in the cache.
// The OpID is a monotonically increasing identifier assigned by the bouncer.
// NodeID identifies the originating node but is not used for ordering.
type Op struct {
	Topic     string
	Key       string
	Value     []byte
	OpID      uint64
	NodeID    uint32
	Tombstone bool
	CreatedAt time.Time // server-side only, used for tombstone TTL expiry
}
