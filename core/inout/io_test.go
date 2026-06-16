package inout

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"testing"
)

func TestEncodeIntToBytes(t *testing.T) {
	var i int32
	i = 1
	bytes := EncodeInt32(i)
	fmt.Println(bytes)
	if len(bytes) != 4 {
		t.Fatalf(`bytes = %q`, bytes)
	}

	if bytes[0] != 1 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[1] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[2] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[3] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
}

func TestEncodeIntToBytes256(t *testing.T) {
	var i int32
	i = 256
	bytes := EncodeInt32(i)
	fmt.Println(bytes)
	if len(bytes) != 4 {
		t.Fatalf(`bytes = %q`, bytes)
	}

	if bytes[0] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[1] != 1 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[2] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
	if bytes[3] != 0 {
		t.Fatalf(`bytes = %q`, bytes)
	}
}

func TestDecodeIntFromBytes(t *testing.T) {
	BBB := []byte{1, 0, 0, 0}

	i := DecodeInt32(BBB)
	if i != 1 {
		t.Fatalf(`i = %d`, i)
	}
}

func TestEncodeIntToFloatSlice(t *testing.T) {
	var i int32
	i = 56
	fs := []float32{0.0, 4.78, 3.99, 2.00004}

	EncodeInt32IntoFloatSlice(fs, i)

	if fs[1] != 4.78 {
		t.Fatalf(`fs = %f`, fs)
	}

	j := decodeInt32FromFloatSlice(fs)

	if j != 56 {
		t.Fatalf(`fs = %d`, j)
	}

}

var result uuid.UUID

func BenchmarkUUIDDeserialise(b *testing.B) {

	var r uuid.UUID

	sl := make([]byte, 512)

	id := uuid.New()

	EncodeUUIDIntoBytesSlice(sl, id)

	for n := 0; n < b.N; n++ {
		// always record the result of Fib to prevent
		// the compiler eliminating the function call.
		r, _ = DecodeUUIDFromBytes(sl)
	}
	// always store the result to a package level variable
	// so the compiler cannot eliminate the Benchmark itself.
	result = r
}

var result2 int32

func BenchmarkIntDeserialise(b *testing.B) {

	var r int32

	sl := make([]byte, 512)

	EncodeInt32IntoByteSlice(sl, 47)

	for n := 0; n < b.N; n++ {
		// always record the result of Fib to prevent
		// the compiler eliminating the function call.
		r = DecodeInt32FromBytes(sl)
	}
	// always store the result to a package level variable
	// so the compiler cannot eliminate the Benchmark itself.
	result2 = r
}

var result3 string

func BenchmarkInfoToJson(b *testing.B) {

	var r string
	id := uuid.New()

	info := common.NodeInfo2{I: id,
		X: 0.576545,
		Y: 0.47654654,
		Z: 0.220987654,
		W: 17.356787,
		P: 6.378547,
		R: 31.7899483,
		V: 0.3678,
		G: 0}

	for n := 0; n < b.N; n++ {
		r = common.NodeInfo2ToString(info)
	}

	result3 = r
	x := []byte(result3)

	fmt.Printf("node: %v\n", result3)

	fmt.Printf("bytes: %d\n", len(x))
}

var result3a common.NodeInfo2

func BenchmarkInfoFromJson(b *testing.B) {

	var r common.NodeInfo2

	info := `{"i":"658d5f30-f1eb-11ee-b938-acde48001122","x":0.576545,"y":0.47654653,"z":0.22098765,"w":17.356787,"p":6.378547,"r":31.789948,"v":0.3678,"g":0}`

	for n := 0; n < b.N; n++ {
		r = common.NodeInfo2FromString(info)
	}

	result3a = r
}

var result4 []byte

func BenchmarkInfoToBytes(b *testing.B) {

	var r []byte

	id := uuid.New()

	info := common.NodeInfo2{I: id,
		X: 0.576545,
		Y: 0.47654654,
		Z: 0.220987654,
		W: 17.356787,
		P: 6.378547,
		R: 31.7899483,
		V: 0.3678,
		G: 0}

	for n := 0; n < b.N; n++ {
		// always record the result of Fib to prevent
		// the compiler eliminating the function call.
		r = NodeInfo2ToBytes(info)
	}
	// always store the result to a package level variable
	// so the compiler cannot eliminate the Benchmark itself.
	result4 = r
}

var result5 common.NodeInfo2

func BenchmarkInfoFromBytes(b *testing.B) {

	var r common.NodeInfo2

	id := uuid.New()

	info := common.NodeInfo2{I: id,
		X: 0.576545,
		Y: 0.47654654,
		Z: 0.220987654,
		W: 17.356787,
		P: 6.378547,
		R: 31.7899483,
		V: 0.3678,
		G: 0}

	x := NodeInfo2ToBytes(info)

	for n := 0; n < b.N; n++ {
		// always record the result of Fib to prevent
		// the compiler eliminating the function call.
		r = NodeInfo2FromBytes(x)
	}
	// always store the result to a package level variable
	// so the compiler cannot eliminate the Benchmark itself.
	result5 = r
}

func TestInfoRoundtrip(t *testing.T) {

	id := uuid.New()

	info := common.NodeInfo2{I: id,
		X: 0.576545,
		Y: 0.47654654,
		Z: 0.220987654,
		W: 17.356787,
		P: 6.378547,
		R: 31.7899483,
		V: 0.3678,
		G: 1}

	asBytes := NodeInfo2ToBytes(info)

	fmt.Printf("asBytes: %v\n", asBytes)

	backAgain := NodeInfo2FromBytes(asBytes)

	fmt.Printf("info: %v\n", info)
	fmt.Printf("back: %v\n", backAgain)

	if info != backAgain {
		t.Fatalf(`failed to round trip info: %v\n%v`, backAgain, info)
	}
}
