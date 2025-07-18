//go:build darwin && cgo
// +build darwin,cgo

package sharedmemory

/*
#include <sys/mman.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>
#include <sys/errno.h>

// _create_shm is called by the "owner" process. It cleans up stale
// segments before creating and sizing a new one.
int _create_shm(const char* name, int size) {
    shm_unlink(name);

    mode_t mode = S_IRUSR | S_IWUSR | S_IRGRP | S_IWGRP;
    int flags = O_RDWR | O_CREAT;

    int fd = shm_open(name, flags, mode);
    if (fd < 0) {
        return -1;
    }

    if (ftruncate(fd, size) != 0) {
        close(fd);
        shm_unlink(name);
        return -2;
    }
    return fd;
}

// _open_shm is called by a client process. It only opens an existing
// segment and MUST NOT call shm_unlink.
int _open_shm(const char* name) {
    int flags = O_RDWR;
    int fd = shm_open(name, flags, 0); // Mode is ignored when not creating
    if (fd < 0) {
        return -1;
    }
    return fd;
}

void* Map(int fd, int size) {
	void* p = mmap(
		NULL, size,
		PROT_READ | PROT_WRITE,
		MAP_SHARED, fd, 0);
	if (p == MAP_FAILED) {
		return NULL;
	}
	return p;
}

void Close(int fd, void* p, int size) {
	if (p != NULL) {
		munmap(p, size);
	}
	if (fd != 0) {
		close(fd);
	}
}

void Delete(const char* name) {
	shm_unlink(name);
}
*/
import "C"

import (
	"fmt"
	"io"
	"unsafe"
)

type shmi struct {
	name   string
	fd     C.int
	v      unsafe.Pointer
	size   int
	parent bool
}

func (o *shmi) getSize() int {
	return o.size
}

func (o *shmi) getPtr() unsafe.Pointer {
	return o.v
}

func create(name string, size int) (*shmi, error) {
	name = "/" + name
	fd := C._create_shm(C.CString(name), C.int(size))
	if fd < 0 {
		return nil, fmt.Errorf("create")
	}
	v := C.Map(fd, C.int(size))
	if v == nil {
		C.Close(fd, nil, C.int(size))
		C.Delete(C.CString(name))
	}
	return &shmi{name, fd, v, size, true}, nil
}

func open(name string, size int) (*shmi, error) {
	name = "/" + name
	fd := C._open_shm(C.CString(name))
	if fd < 0 {
		return nil, fmt.Errorf("open")
	}
	v := C.Map(fd, C.int(size))
	if v == nil {
		C.Close(fd, nil, C.int(size))
	}
	return &shmi{name, fd, v, size, false}, nil
}

func (o *shmi) close() error {
	if o.v != nil {
		C.Close(o.fd, o.v, C.int(o.size))
		o.v = nil
	}
	if o.parent {
		C.Delete(C.CString(o.name))
	}
	return nil
}

func (o *shmi) readAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copyPtr2Slice(uintptr(o.v), p, off, o.size), nil
}

func (o *shmi) writeAt(p []byte, off int64) (n int, err error) {
	if off >= int64(o.size) {
		return 0, io.EOF
	}
	if max := int64(o.size) - off; int64(len(p)) > max {
		p = p[:max]
	}
	return copySlice2Ptr(p, uintptr(o.v), off, o.size), nil
}
