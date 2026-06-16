package statecache

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Wire format for cached state operations.
//
// Byte layout (big-endian):
//
//	[Version    1 byte]  — 0xCA identifies a cache envelope
//	[Flags      1 byte]  — bit 0: tombstone
//	[OpID       8 bytes] — monotonic operation ID assigned by the bouncer
//	[NodeID     4 bytes] — originating node ID (for identification, not ordering)
//	[TopicLen   1 byte]  — length of topic string
//	[Topic      N bytes] — topic string (e.g. "attributes")
//	[KeyLen     2 bytes] — length of key string
//	[Key        N bytes] — cache key (e.g. node UUID)
//	[ValueLen   4 bytes] — length of value payload
//	[Value      N bytes] — original message payload
//
// Minimum envelope size: 21 bytes (all variable fields empty).

const (
	wireVersion   byte = 0xCA
	minWireLen         = 1 + 1 + 8 + 4 + 1 + 0 + 2 + 0 + 4 + 0 // 21 bytes
	flagTombstone byte = 0x01
)

var (
	ErrTooShort           = errors.New("statecache: envelope too short")
	ErrUnsupportedVersion = errors.New("statecache: unsupported envelope version")
	ErrTopicOverrun       = errors.New("statecache: topic length exceeds remaining bytes")
	ErrKeyOverrun         = errors.New("statecache: key length exceeds remaining bytes")
	ErrValueOverrun       = errors.New("statecache: value length exceeds remaining bytes")
)

// Encode serialises an Op into the wire format. The returned byte slice is
// exactly the right size with no wasted capacity.
func Encode(op Op) ([]byte, error) {
	topicLen := len(op.Topic)
	if topicLen > 255 {
		return nil, fmt.Errorf("statecache: topic too long (%d bytes, max 255)", topicLen)
	}
	keyLen := len(op.Key)
	if keyLen > 65535 {
		return nil, fmt.Errorf("statecache: key too long (%d bytes, max 65535)", keyLen)
	}

	size := 1 + 1 + 8 + 4 + 1 + topicLen + 2 + keyLen + 4 + len(op.Value)
	buf := make([]byte, size)
	off := 0

	buf[off] = wireVersion
	off++

	var flags byte
	if op.Tombstone {
		flags |= flagTombstone
	}
	buf[off] = flags
	off++

	binary.BigEndian.PutUint64(buf[off:], op.OpID)
	off += 8

	binary.BigEndian.PutUint32(buf[off:], op.NodeID)
	off += 4

	buf[off] = byte(topicLen)
	off++
	copy(buf[off:], op.Topic)
	off += topicLen

	binary.BigEndian.PutUint16(buf[off:], uint16(keyLen))
	off += 2
	copy(buf[off:], op.Key)
	off += keyLen

	binary.BigEndian.PutUint32(buf[off:], uint32(len(op.Value)))
	off += 4
	copy(buf[off:], op.Value)

	return buf, nil
}

// Decode deserialises an Op from the wire format.
func Decode(data []byte) (Op, error) {
	if len(data) < minWireLen {
		return Op{}, ErrTooShort
	}

	off := 0

	if data[off] != wireVersion {
		return Op{}, fmt.Errorf("%w: got 0x%02X, want 0x%02X", ErrUnsupportedVersion, data[off], wireVersion)
	}
	off++

	flags := data[off]
	off++

	opID := binary.BigEndian.Uint64(data[off:])
	off += 8

	nodeID := binary.BigEndian.Uint32(data[off:])
	off += 4

	topicLen := int(data[off])
	off++
	if off+topicLen > len(data) {
		return Op{}, ErrTopicOverrun
	}
	topic := string(data[off : off+topicLen])
	off += topicLen

	if off+2 > len(data) {
		return Op{}, ErrTooShort
	}
	keyLen := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	if off+keyLen > len(data) {
		return Op{}, ErrKeyOverrun
	}
	key := string(data[off : off+keyLen])
	off += keyLen

	if off+4 > len(data) {
		return Op{}, ErrTooShort
	}
	valueLen := int(binary.BigEndian.Uint32(data[off:]))
	off += 4
	if off+valueLen > len(data) {
		return Op{}, ErrValueOverrun
	}
	var value []byte
	if valueLen > 0 {
		value = make([]byte, valueLen)
		copy(value, data[off:off+valueLen])
	}

	return Op{
		Topic:     topic,
		Key:       key,
		Value:     value,
		OpID:      opID,
		NodeID:    nodeID,
		Tombstone: flags&flagTombstone != 0,
	}, nil
}

// IsCacheEnvelope returns true if the data starts with the cache version byte.
// This allows callers to distinguish cached envelopes from plain messages
// without fully decoding.
func IsCacheEnvelope(data []byte) bool {
	return len(data) > 0 && data[0] == wireVersion
}
