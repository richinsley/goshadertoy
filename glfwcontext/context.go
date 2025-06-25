package glfwcontext

import (
	"log"
	"runtime"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/go-gl/glfw/v3.3/glfw"
)

// Context is a dedicated package for managing the GLFW window and context.
// This is the ONLY package in the project that should import glfw.
type Context struct {
	window *glfw.Window
}

// New creates and initializes a new GLFW context and window.
func NewContext() (*Context, error) {
	// All GLFW calls that can only run on the main thread are here.
	runtime.LockOSThread()

	if err := glfw.Init(); err != nil {
		return nil, err
	}

	glfw.WindowHint(glfw.ContextVersionMajor, 4)
	glfw.WindowHint(glfw.ContextVersionMinor, 1)
	glfw.WindowHint(glfw.OpenGLProfile, glfw.OpenGLCoreProfile)
	glfw.WindowHint(glfw.OpenGLForwardCompatible, glfw.True)
	glfw.WindowHint(glfw.Resizable, glfw.True)

	win, err := glfw.CreateWindow(1280, 720, "goshadertoy", nil, nil)
	if err != nil {
		glfw.Terminate()
		return nil, err
	}

	win.MakeContextCurrent()

	// gl.Init also needs to be called after a context is made current.
	if err := gl.Init(); err != nil {
		return nil, err
	}
	log.Printf("GLFW Context: OpenGL Version %s", gl.GoStr(gl.GetString(gl.VERSION)))

	return &Context{window: win}, nil
}

// Shutdown safely terminates the GLFW context.
func (c *Context) Shutdown() {
	glfw.Terminate()
}

// ShouldClose returns true if the user has requested to close the window.
func (c *Context) ShouldClose() bool {
	return c.window.ShouldClose()
}

// EndFrame swaps the graphics buffers and polls for user events.
func (c *Context) EndFrame() {
	c.window.SwapBuffers()
	glfw.PollEvents()
}

// GetFramebufferSize returns the current width and height of the window's drawable area.
func (c *Context) GetFramebufferSize() (int, int) {
	return c.window.GetFramebufferSize()
}

// Window returns the underlying *glfw.Window object for direct access if needed (e.g., input).
func (c *Context) Window() *glfw.Window {
	return c.window
}

// Time returns the number of seconds since the context was initialized.
func (c *Context) Time() float64 {
	return glfw.GetTime()
}
