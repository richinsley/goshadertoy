package glfwcontext

import (
	"log"
	"runtime"

	glfw "github.com/go-gl/glfw/v3.3/glfw"
	options "github.com/richinsley/goshadertoy/options"
)

// Context now tracks mouse state for the GetMouseInput method.
type Context struct {
	window          *glfw.Window
	lastMouseClickX float64
	lastMouseClickY float64
	mouseWasDown    bool
	// A map to store functions to be called on key presses.
	keyCallbacks map[glfw.Key]func()
}

// New creates and initializes a new GLFW window and returns a Context object.
// func New(width, height int, visible bool, share interface{}) (*Context, error) {
func New(options *options.ShaderOptions, visible bool, share interface{}) (*Context, error) {
	sharecontext, _ := share.(*glfw.Window)
	glfw.WindowHint(glfw.ContextVersionMajor, 4)
	glfw.WindowHint(glfw.ContextVersionMinor, 1)
	glfw.WindowHint(glfw.OpenGLProfile, glfw.OpenGLCoreProfile)
	glfw.WindowHint(glfw.OpenGLForwardCompatible, glfw.True)

	if *options.BitDepth > 8 {
		glfw.WindowHint(glfw.RedBits, 16)
		glfw.WindowHint(glfw.GreenBits, 16)
		glfw.WindowHint(glfw.BlueBits, 16)
	}

	if visible {
		glfw.WindowHint(glfw.Resizable, glfw.True)
	} else {
		glfw.WindowHint(glfw.Visible, glfw.False)
	}

	win, err := glfw.CreateWindow(*options.Width, *options.Height, "goshadertoy", nil, sharecontext)
	if err != nil {
		return nil, err
	}

	c := &Context{
		window:       win,
		keyCallbacks: make(map[glfw.Key]func()),
	}

	// Set the key callback for the window to be the method on our new context instance.
	win.SetKeyCallback(c.glfwKeyCallback)

	return c, nil
}

// RegisterKeyCallback allows the main application to register a function to be
// called when a specific key is pressed.
func (c *Context) RegisterKeyCallback(key glfw.Key, f func()) {
	c.keyCallbacks[key] = f
}

// glfwKeyCallback is the function that will be called by GLFW on a key event.
// It now dispatches to our registered custom callbacks.
func (c *Context) glfwKeyCallback(w *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	// Handle the default Escape key behavior
	if key == glfw.KeyEscape && action == glfw.Press {
		w.SetShouldClose(true)
	}

	// If a key is pressed and we have a callback for it, run it.
	if action == glfw.Press {
		if callback, ok := c.keyCallbacks[key]; ok {
			callback()
		}
	}
}

// DetachCurrent makes no context current on the calling thread.
func (c *Context) DetachCurrent() {
	glfw.DetachCurrentContext()
}

func (c *Context) IsGLES() bool {
	// GLFW does not provide a direct way to check if the context is GLES.
	return false
}

// GetWindow returns the underlying *glfw.Window. This is kept for the sound-context sharing case.
func (c *Context) GetWindow() interface{} {
	return c.window
}

// GetMouseInput implements the method for the graphics.Context interface.
// It retrieves and processes the current mouse state.
func (c *Context) GetMouseInput() [4]float32 {
	var mouseData [4]float32
	if c.window == nil {
		return mouseData
	}

	fbWidth, fbHeight := c.GetFramebufferSize()
	winWidth, winHeight := c.window.GetSize()
	var scaleX, scaleY float64 = 1.0, 1.0
	if winWidth > 0 && winHeight > 0 {
		scaleX = float64(fbWidth) / float64(winWidth)
		scaleY = float64(fbHeight) / float64(winHeight)
	}

	cursorX, cursorY := c.window.GetCursorPos()
	pixelX := cursorX * scaleX
	pixelY := cursorY * scaleY

	mouseX := float32(pixelX)
	mouseY := float32(fbHeight) - float32(pixelY)

	const mouseLeft = 0
	isMouseDown := c.window.GetMouseButton(mouseLeft) == glfw.Press
	if isMouseDown && !c.mouseWasDown {
		c.lastMouseClickX = pixelX
		c.lastMouseClickY = pixelY
	}
	c.mouseWasDown = isMouseDown

	clickX := float32(c.lastMouseClickX)
	clickY := float32(fbHeight) - float32(c.lastMouseClickY)

	if !isMouseDown {
		clickX = -clickX
		clickY = -clickY
	}

	mouseData = [4]float32{mouseX, mouseY, clickX, clickY}
	return mouseData
}

// MakeCurrent makes the context current for the calling goroutine.
func (c *Context) MakeCurrent() {
	c.window.MakeContextCurrent()
}

// Shutdown now only destroys the window.
func (c *Context) Shutdown() {
	c.window.Destroy()
}

func (c *Context) ShouldClose() bool {
	return c.window.ShouldClose()
}

func (c *Context) EndFrame() {
	c.window.SwapBuffers()
	glfw.PollEvents()
}

func (c *Context) GetFramebufferSize() (int, int) {
	return c.window.GetFramebufferSize()
}

func (c *Context) Time() float64 {
	return glfw.GetTime()
}

// Window returns the underlying *glfw.Window. This is kept for the sound-context sharing case.
func (c *Context) Window() *glfw.Window {
	return c.window
}

// InitGraphics initializes the main graphics subsystem (GLFW). Must be called from the main thread.
func InitGraphics() error {
	runtime.LockOSThread()
	if err := glfw.Init(); err != nil {
		return err
	}
	log.Printf("GLFW Initialized")
	return nil
}

// TerminateGraphics shuts down the graphics subsystem. Must be called from the main thread.
func TerminateGraphics() {
	glfw.Terminate()
	log.Printf("GLFW Terminated")
}
