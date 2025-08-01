package graphics

// Context defines the interface for an OpenGL context.
type Context interface {
	MakeCurrent()
	Shutdown()
	ShouldClose() bool
	EndFrame()
	GetFramebufferSize() (int, int)
	Time() float64
	// GetMouseInput returns the current mouse state: x, y, clickX, clickY
	GetMouseInput() [4]float32
}
