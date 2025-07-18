package sharedmemory

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

type shmi struct {
	h    syscall.Handle
	v    uintptr
	size int
}

func (o *shmi) getSize() int {
	return o.size
}

func (o *shmi) getPtr() unsafe.Pointer {
	return unsafe.Pointer(o.v)
}

// create is called by the "owner" of the shared memory. It creates a new
// file mapping object.
func create(name string, size int) (*shmi, error) {
	key, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	h, err := syscall.CreateFileMapping(
		syscall.InvalidHandle, nil,
		syscall.PAGE_READWRITE, 0, uint32(size), key)
	if err != nil {
		return nil, os.NewSyscallError("CreateFileMapping", err)
	}

	v, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_WRITE, 0, 0, 0)
	if err != nil {
		syscall.CloseHandle(h)
		return nil, os.NewSyscallError("MapViewOfFile", err)
	}

	return &shmi{h, v, size}, nil
}

// open is called by a "client". It opens an *existing* file mapping object
// and must not try to create a new one.
func open(name string, size int) (*shmi, error) {
	key, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	// Use CreateFileMapping with size 0 to open existing mapping
	h, err := syscall.CreateFileMapping(
		syscall.InvalidHandle, nil,
		syscall.PAGE_READWRITE, 0, 0, key) // size 0 opens existing
	if err != nil {
		return nil, os.NewSyscallError("CreateFileMapping", err)
	}

	v, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_WRITE, 0, 0, 0)
	if err != nil {
		syscall.CloseHandle(h)
		return nil, os.NewSyscallError("MapViewOfFile", err)
	}

	return &shmi{h, v, size}, nil
}

func (o *shmi) close() error {
	if o.v != uintptr(0) {
		syscall.UnmapViewOfFile(o.v)
		o.v = uintptr(0)
	}
	if o.h != syscall.InvalidHandle {
		syscall.CloseHandle(o.h)
		o.h = syscall.InvalidHandle
	}
	return nil
}

// read shared memory. return read size.
func (o *shmi) readAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copyPtr2Slice(o.v, p, off, o.size), nil
}

// write shared memory. return write size.
func (o *shmi) writeAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copySlice2Ptr(p, o.v, off, o.size), nil
}
