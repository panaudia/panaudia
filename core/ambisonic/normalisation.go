package ambisonic

import (
	"github.com/panaudia/panaudia/spacer"
	"unsafe"
)

func ConvertN3DtoSN3DInPlace(src []float32, order int, size int) {
	p := uintptr(unsafe.Pointer(&src[0]))
	spacer.Panaudia_utils_convertN3DToSN3D(p, order, size)
}
