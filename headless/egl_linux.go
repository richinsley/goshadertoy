//go:build linux

package headless

import (
	"fmt"
	"log"
	"unsafe"

	gl "github.com/go-gl/gl/v4.1-core/gl"
)

/*
#cgo LDFLAGS: -lEGL -lGLESv2
#include <EGL/egl.h>
#include <EGL/eglext.h>

// Go doesn't have a great way to call function pointers from C,
// so we'll create simple wrappers for the extension functions.
static PFNEGLQUERYDEVICESEXTPROC eglQueryDevicesEXT_ptr = NULL;
static PFNEGLGETPLATFORMDISPLAYEXTPROC eglGetPlatformDisplayEXT_ptr = NULL;

static void initialize_egl_extension_pointers() {
    eglQueryDevicesEXT_ptr = (PFNEGLQUERYDEVICESEXTPROC) eglGetProcAddress("eglQueryDevicesEXT");
    eglGetPlatformDisplayEXT_ptr = (PFNEGLGETPLATFORMDISPLAYEXTPROC) eglGetProcAddress("eglGetPlatformDisplayEXT");
}

static EGLDisplay get_platform_display(EGLenum platform, void *native_display, const EGLint *attrib_list) {
    if (eglGetPlatformDisplayEXT_ptr) {
        return eglGetPlatformDisplayEXT_ptr(platform, native_display, attrib_list);
    }
    return EGL_NO_DISPLAY;
}

static EGLBoolean query_devices(EGLint max_devices, EGLDeviceEXT *devices, EGLint *num_devices) {
    if (eglQueryDevicesEXT_ptr) {
        return eglQueryDevicesEXT_ptr(max_devices, devices, num_devices);
    }
    return EGL_FALSE;
}
*/
import "C"

type Headless struct {
	display C.EGLDisplay
	context C.EGLContext
	surface C.EGLSurface
}

// getEGLDisplay tries the robust device enumeration method first,
// falling back to the default display.
func getEGLDisplay() (C.EGLDisplay, error) {
	C.initialize_egl_extension_pointers()

	var num_devices C.EGLint
	// First, query for the number of devices.
	if C.query_devices(0, nil, &num_devices) == C.EGL_FALSE || num_devices == 0 {
		log.Println("Warning: EGL_EXT_device_query not supported or no devices found. Falling back to EGL_DEFAULT_DISPLAY.")
		display := C.eglGetDisplay(C.EGLNativeDisplayType(C.EGL_DEFAULT_DISPLAY))
		if display == C.EGLDisplay(C.EGL_NO_DISPLAY) {
			return C.EGLDisplay(C.EGL_NO_DISPLAY), fmt.Errorf("fallback to eglGetDisplay(EGL_DEFAULT_DISPLAY) failed")
		}
		return display, nil
	}

	log.Printf("Found %d EGL device(s).", num_devices)
	devices := make([]C.EGLDeviceEXT, num_devices)

	// Get the device handles.
	if C.query_devices(num_devices, &devices[0], &num_devices) == C.EGL_FALSE {
		return C.EGLDisplay(C.EGL_NO_DISPLAY), fmt.Errorf("failed to query EGL devices")
	}

	// Iterate through the devices and get a display from the first one that works.
	// In an NVIDIA Docker container, this will be the NVIDIA GPU.
	for i := 0; i < int(num_devices); i++ {
		display := C.get_platform_display(C.EGL_PLATFORM_DEVICE_EXT, unsafe.Pointer(devices[i]), nil)
		if display != C.EGLDisplay(C.EGL_NO_DISPLAY) {
			log.Printf("Successfully got EGL display from device %d.", i)
			return display, nil
		}
	}

	return C.EGLDisplay(C.EGL_NO_DISPLAY), fmt.Errorf("could not get a valid EGL display from any available device")
}

func NewHeadless(width, height int) (*Headless, error) {
	h := &Headless{}

	var err error
	h.display, err = getEGLDisplay()
	if err != nil {
		return nil, fmt.Errorf("failed to get EGL display: %w", err)
	}

	var major, minor C.EGLint
	if C.eglInitialize(h.display, &major, &minor) == C.EGL_FALSE {
		return nil, fmt.Errorf("failed to initialize EGL")
	}
	log.Printf("EGL Initialized. Version: %d.%d", major, minor)

	configAttribs := []C.EGLint{
		C.EGL_SURFACE_TYPE, C.EGL_PBUFFER_BIT,
		C.EGL_RED_SIZE, 8,
		C.EGL_GREEN_SIZE, 8,
		C.EGL_BLUE_SIZE, 8,
		C.EGL_ALPHA_SIZE, 8,
		C.EGL_DEPTH_SIZE, 24,
		C.EGL_RENDERABLE_TYPE, C.EGL_OPENGL_ES3_BIT,
		C.EGL_NONE,
	}

	var config C.EGLConfig
	var numConfig C.EGLint
	if C.eglChooseConfig(h.display, &configAttribs[0], &config, 1, &numConfig) == C.EGL_FALSE || numConfig == 0 {
		return nil, fmt.Errorf("failed to choose EGL config")
	}

	pbufferAttribs := []C.EGLint{
		C.EGL_WIDTH, C.EGLint(width),
		C.EGL_HEIGHT, C.EGLint(height),
		C.EGL_NONE,
	}
	h.surface = C.eglCreatePbufferSurface(h.display, config, &pbufferAttribs[0])
	if h.surface == C.EGLSurface(C.EGL_NO_SURFACE) {
		return nil, fmt.Errorf("failed to create Pbuffer surface")
	}

	contextAttribs := []C.EGLint{
		C.EGL_CONTEXT_CLIENT_VERSION, 3,
		C.EGL_NONE,
	}
	h.context = C.eglCreateContext(h.display, config, C.EGLContext(C.EGL_NO_CONTEXT), &contextAttribs[0])
	if h.context == C.EGLContext(C.EGL_NO_CONTEXT) {
		return nil, fmt.Errorf("failed to create EGL context")
	}

	if C.eglMakeCurrent(h.display, h.surface, h.surface, h.context) == C.EGL_FALSE {
		return nil, fmt.Errorf("failed to make EGL context current")
	}

	if err := gl.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL ES: %w", err)
	}

	return h, nil
}

func (h *Headless) Shutdown() {
	if h.display != C.EGLDisplay(C.EGL_NO_DISPLAY) {
		C.eglMakeCurrent(h.display, C.EGLSurface(C.EGL_NO_SURFACE), C.EGLSurface(C.EGL_NO_SURFACE), C.EGLContext(C.EGL_NO_CONTEXT))
		if h.context != C.EGLContext(C.EGL_NO_CONTEXT) {
			C.eglDestroyContext(h.display, h.context)
		}
		if h.surface != C.EGLSurface(C.EGL_NO_SURFACE) {
			C.eglDestroySurface(h.display, h.surface)
		}
		C.eglTerminate(h.display)
	}
}

func (h *Headless) SwapBuffers() {
	C.eglSwapBuffers(h.display, h.surface)
}
