package inout

//https://www.reddit.com/r/golang/comments/qctyhx/what_is_the_best_way_to_covert_slice_of_floats_to/

import (
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"unsafe"
)

func DecodeInt32(bs []byte) int32 {
	return *(*int32)(unsafe.Pointer(&bs[0]))
}

func DecodeUint64(bs []byte) uint64 {
	return *(*uint64)(unsafe.Pointer(&bs[0]))
}

func EncodeInt32(number int32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&number)), 4)
}

func EncodeUint64(number uint64) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&number)), 8)
}

func EncodeInt32IntoFloatSlice(fs []float32, number int32) {
	floatsAsBytes := encodeUnsafe(fs)
	numAsBytes := EncodeInt32(number)
	copy(floatsAsBytes, numAsBytes)
}

func EncodeInt32IntoByteSlice(bs []byte, number int32) {
	numAsBytes := EncodeInt32(number)
	copy(bs, numAsBytes)
}

func EncodeUint64IntoByteSlice(bs []byte, number uint64) {
	numAsBytes := EncodeUint64(number)
	copy(bs, numAsBytes)
}

func EncodeUUIDIntoFloatSlice(fs []float32, id uuid.UUID) {
	floatsAsBytes := encodeUnsafe(fs)
	idAsBytes, _ := id.MarshalBinary()
	copy(floatsAsBytes, idAsBytes)
}

func EncodeUUIDIntoBytesSlice(bs []byte, id uuid.UUID) {
	idAsBytes, _ := id.MarshalBinary()
	copy(bs, idAsBytes)
}

func DecodeUUIDFromBytes(bs []byte) (uuid.UUID, error) {
	UUID := uuid.New()
	err := (&UUID).UnmarshalBinary(bs[:16])
	return UUID, err
}

func DecodeInt32FromBytes(bs []byte) int32 {
	return DecodeInt32(bs)
}

func decodeInt32FromFloatSlice(fs []float32) int32 {
	floatsAsBytes := encodeUnsafe(fs)
	return DecodeInt32(floatsAsBytes[:4])
}

func encodef32(fs []float32) []byte {
	return encodeUnsafe(fs)
}

func decodef32(bs []byte) []float32 {
	return decodeUnsafe(bs)
}

func Encodef32(fs []float32) []byte {
	return encodeUnsafe(fs)
}

func Decodef32(bs []byte) []float32 {
	return decodeUnsafe(bs)
}

func AsUnsafeByteSlice(fs []float32) []byte {
	return encodeUnsafe(fs)
}

func encodeUnsafe(fs []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&fs[0])), len(fs)*4)
}

func DemungeInt16Bigendian(src []float32) []float32 {

	asBytes := encodeUnsafe(src)
	dst := make([]float32, len(src)*2, len(src)*2)

	for i := 0; i < len(src)*2; i++ {
		dst[i] = ((float32(asBytes[(i*2)+1]) + float32(asBytes[(i*2)])*256.0) / 32768.0) - 1
	}
	return dst
}

func decodeUnsafe(bs []byte) []float32 {
	return unsafe.Slice((*float32)(unsafe.Pointer(&bs[0])), len(bs)/4)
}

func NodeInfo2ToBytes(node common.NodeInfo2) []byte {
	r := make([]byte, 48)
	b, _ := node.I.MarshalBinary()
	copy(r[:16], b)                                                         // 16 bytes * 8 = 128
	fs := []float32{node.X, node.Y, node.Z, node.W, node.P, node.R, node.V} // 7 * 4 = 28 + 16 = 44
	copy(r[16:], Encodef32(fs))
	copy(r[44:], EncodeInt32(node.G))
	return r
}

func NodeInfo2FromBytes(b []byte) common.NodeInfo2 {

	id := uuid.UUID{}
	err := (&id).UnmarshalBinary(b[:16])
	if err != nil {
		panic("failed to get uuid out of bytes")
	}

	asFloats := decodef32(b)

	info := common.NodeInfo2{I: id,
		X: asFloats[4],
		Y: asFloats[5],
		Z: asFloats[6],
		W: asFloats[7],
		P: asFloats[8],
		R: asFloats[9],
		V: asFloats[10],
		G: DecodeInt32(b[44:])}

	return info
}

func NodeInfo3FromBytes(b []byte) common.NodeInfo3 {

	id := uuid.UUID{}
	err := (&id).UnmarshalBinary(b[:16])
	if err != nil {
		panic("failed to get uuid out of bytes")
	}

	asFloats := decodef32(b)

	// Gone is deliberately NOT decoded. This decoder's production
	// callers are (a) inbound client state (state_input_handler, the
	// WebRTC state channel), where a client-supplied Gone must never be
	// trusted — departure is server-inferred only (plan/history/state-cleanup
	// Q3) — and (b) the outbound writers, which read only Uuid for the
	// visibility filter and forward the raw bytes unchanged. Tests that
	// need the wire value use DecodeInt32(b[44:]) directly.
	info := common.NodeInfo3{Uuid: id,
		Position: common.Position{X: float64(asFloats[4]), Y: float64(asFloats[5]), Z: float64(asFloats[6])},
		Rotation: common.Rotation{Yaw: float64(asFloats[7]), Pitch: float64(asFloats[8]), Roll: float64(asFloats[9])},
		Volume:   float64(asFloats[10]),
		Gone:     0}

	return info
}

func NodeInfo3ToBytes(node common.NodeInfo3) []byte {
	r := make([]byte, 48)
	b, _ := node.Uuid.MarshalBinary()
	copy(r[:16], b) // 16 bytes * 8 = 128
	fs := []float32{float32(node.Position.X),
		float32(node.Position.Y),
		float32(node.Position.Z),
		float32(node.Rotation.Yaw),
		float32(node.Rotation.Pitch),
		float32(node.Rotation.Roll),
		float32(node.Volume)} // 7 * 4 = 28 + 16 = 44
	copy(r[16:], Encodef32(fs))
	copy(r[44:], EncodeInt32(node.Gone))
	return r
}
