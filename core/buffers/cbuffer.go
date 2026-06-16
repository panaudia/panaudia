package buffers

import (
	"github.com/panaudia/panaudia/spacer"
	"unsafe"
)

type CBuffer struct {
	p_data    uintptr
	p_scratch uintptr
	count     int
	isChild   bool
}

func (buffer *CBuffer) GetDataPointer() uintptr {
	return buffer.p_data
}

func (buffer *CBuffer) GetSize() int {
	return buffer.count
}

func NewCBuffer(size int) *CBuffer {
	cbuffer := CBuffer{}
	cbuffer.count = size
	cbuffer.isChild = false
	p := uintptr(0)
	spacer.Panaudia_utils_create_buffer(&p, size)
	cbuffer.p_data = p

	q := uintptr(0)
	spacer.Panaudia_utils_create_buffer(&q, size)
	cbuffer.p_scratch = q

	spacer.Panaudia_utils_clear_buffer(cbuffer.p_data, cbuffer.count)

	return &cbuffer
}

// the data is not allocated but uses the parent buffer's data
func NewChildCBuffer(size int, parent *CBuffer, position int) *CBuffer {
	cbuffer := CBuffer{}
	cbuffer.count = size
	cbuffer.isChild = true

	if position+size > parent.count {
		panic("NewChildCBuffer going past end of parent data")
	}

	cbuffer.p_data = uintptr(int(parent.GetDataPointer()) + position*4)

	q := uintptr(0)
	spacer.Panaudia_utils_create_buffer(&q, size)
	cbuffer.p_scratch = q

	return &cbuffer
}

func (buffer *CBuffer) BeforeDestroy() {
	if !buffer.isChild {
		spacer.Panaudia_utils_delete_buffer(&buffer.p_data)
	}
	spacer.Panaudia_utils_delete_buffer(&buffer.p_scratch)
}

func (buffer *CBuffer) Clear() {
	spacer.Panaudia_utils_clear_buffer(buffer.p_data, buffer.count)
}

func (buffer *CBuffer) CopyFromSlice(src []float32) {
	p := uintptr(unsafe.Pointer(&src[0]))
	spacer.Panaudia_utils_copy_buffer(p, buffer.p_data, buffer.count)
}

func (buffer *CBuffer) CopyFromIntSlice(src []int32) {
	p := uintptr(unsafe.Pointer(&src[0]))
	spacer.Panaudia_utils_copy_buffer(p, buffer.p_data, buffer.count)
	//copy(buffer.AsUnsafeInt32Slice(), src)
}

func (buffer *CBuffer) CopyToSlice(dst []float32) {
	p := uintptr(unsafe.Pointer(&dst[0]))
	spacer.Panaudia_utils_copy_buffer(buffer.p_data, p, buffer.count)
}

func (buffer *CBuffer) CopyFromCBuffer(other *CBuffer) {
	spacer.Panaudia_utils_copy_buffer(other.p_data, buffer.p_data, buffer.count)
}

func (buffer *CBuffer) AddCBuffer(other *CBuffer) {
	spacer.Panaudia_utils_copy_buffer(buffer.p_data, buffer.p_scratch, buffer.count)
	spacer.Panaudia_utils_add_buffer(other.p_data, buffer.p_scratch, buffer.p_data, buffer.count)
}

func (buffer *CBuffer) AddCBufferLength(other *CBuffer, length int) {
	spacer.Panaudia_utils_copy_buffer(buffer.p_data, buffer.p_scratch, length)
	spacer.Panaudia_utils_add_buffer(other.p_data, buffer.p_scratch, buffer.p_data, length)
}

func (buffer *CBuffer) InterleaveCBuffers(otherA *CBuffer, otherB *CBuffer) {
	spacer.Panaudia_utils_interleave_buffers(otherA.p_data, otherB.p_data, buffer.p_data, buffer.count/2)
}

//
//func (buffer *CBuffer) InterleaveChunks(other *CBuffer, channels int, frame int) {
//	spacer.Panaudia_utils_interleave_buffers(otherA.p_data, otherB.p_data, buffer.p_data, buffer.count/2)
//}

func (buffer *CBuffer) SumInterleavedCBuffer(other *CBuffer) {
	spacer.Panaudia_utils_sum_interleaved_buffer(other.p_data, buffer.p_data, buffer.count)
}

func (buffer *CBuffer) Scale(scale float32) {
	spacer.Panaudia_utils_scale_buffer(buffer.p_data, scale, buffer.count)
}

func (buffer *CBuffer) AsUnsafeByteSlice() []byte {
	//     return unsafe.Slice((*byte)(buffer.p_data), buffer.size)
	return unsafe.Slice((*byte)(unsafe.Pointer(buffer.p_data)), buffer.count*4)
}

func (buffer *CBuffer) AsUnsafeFloatSlice() []float32 {
	//     return unsafe.Slice((*byte)(buffer.p_data), buffer.size)
	return unsafe.Slice((*float32)(unsafe.Pointer(buffer.p_data)), buffer.count)
}

func (buffer *CBuffer) AsUnsafeInt32Slice() []int32 {
	//     return unsafe.Slice((*byte)(buffer.p_data), buffer.size)
	return unsafe.Slice((*int32)(unsafe.Pointer(buffer.p_data)), buffer.count)
}
