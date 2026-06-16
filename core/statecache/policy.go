package statecache

import (
	"encoding/json"
)

// CachePolicy determines whether a given topic should be cached.
//
// To add a new cacheable topic:
//  1. Write a CachePolicy that returns true for your topic (and "attributes"):
//     func MyPolicy() CachePolicy {
//         return func(topic string) bool {
//             return topic == "attributes" || topic == "my-topic"
//         }
//     }
//  2. Write a KeyExtractor that handles your topic's message format:
//     func MyKeyExtractor() KeyExtractor {
//         return func(topic string, msg []byte) (string, bool, bool) {
//             switch topic {
//             case "attributes":
//                 return extractOpKey(msg)
//             case "my-topic":
//                 return extractOpKey(msg)
//             default:
//                 return "", false, false
//             }
//         }
//     }
//  3. Set both on the DirectBackend (standalone) or bouncer server (cloud):
//     backend.CachePolicy = MyPolicy()
//     backend.KeyExtractor = MyKeyExtractor()
type CachePolicy func(topic string) bool

// KeyExtractor extracts the cache key from a single JSON operation message.
// Returns the key, whether the message is a tombstone, and true on success.
// Returns empty string, false, false if the message cannot be parsed
// (in which case it should be forwarded uncached).
//
// The KeyExtractor handles individual operations only. For batches,
// the caller (backend write path) splits the batch first and calls
// the extractor once per individual operation.
type KeyExtractor func(topic string, msg []byte) (key string, tombstone bool, ok bool)

// DefaultPolicy caches "attributes", "entity" and "space" messages.
//
// "entity" carries server-internal per-node config (currently only
// `subspaces`) used by gateways to filter what to forward to their
// connected client. Like attributes it is cached so that gateways
// joining mid-session pick up state via backfill.
//
// "space" carries space-wide role-rule state written by the eight
// `space.role.*` commands (see core/commands/defs.go) — keys are of
// the form `roles-muted.{role}`, `roles-kicked.{role}`, etc., with no
// uuid prefix since the record is space-wide rather than per-entity.
// Cached so connecting clients backfill the current rules, gated for
// delivery by the `space.read` cap (see commands.ReadCapSpaceRead).
func DefaultPolicy() CachePolicy {
	return func(topic string) bool {
		return topic == "attributes" || topic == "entity" || topic == "space"
	}
}

// DefaultKeyExtractor extracts the "key" field from a JSON operation.
// Operations use the format: {"key":"uuid.field","value":...} or
// {"key":"uuid.field","tombstone":true}. The extractor reads only "key"
// and "tombstone", ignoring "value" entirely.
//
// All three default cacheable topics use the same op format; "space"
// keys differ only in lacking a leading uuid (`roles-muted.{role}`
// rather than `{uuid}.field`). The extractor doesn't interpret the
// key's structure, so the same body works.
func DefaultKeyExtractor() KeyExtractor {
	return func(topic string, msg []byte) (string, bool, bool) {
		switch topic {
		case "attributes", "entity", "space":
			return extractOpKey(msg)
		}
		return "", false, false
	}
}

// extractOpKey reads the "key" and "tombstone" fields from a single
// JSON operation message. It does not read or interpret the "value" field.
func extractOpKey(msg []byte) (key string, tombstone bool, ok bool) {
	var op JsonOp
	if err := json.Unmarshal(msg, &op); err != nil {
		return "", false, false
	}
	if op.Key == "" {
		return "", false, false
	}
	return op.Key, op.Tombstone, true
}
