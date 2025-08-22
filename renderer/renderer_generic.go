//go:build !linux

package renderer

import (
	"fmt"
	"sync" // Import the sync package

	gl "github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/audio"
	"github.com/richinsley/goshadertoy/graphics"
	inputs "github.com/richinsley/goshadertoy/inputs"
)

// Add the same sync.Once variable here.
var glInitOnce sync.Once

// Renderer struct for non-Linux platforms.
type Renderer struct {
	context           graphics.Context
	quadVAO           uint32
	bufferPasses      []*RenderPass
	namedPasses       map[string]*RenderPass
	buffers           map[string]*inputs.Buffer
	offscreenRenderer *OffscreenRenderer
	blitProgram       uint32
	yuvProgram        uint32
	yuvBitDepthLoc    int32
	width             int
	height            int
	recordMode        bool
	audioDevice       audio.AudioDevice
}

func NewRenderer(width, height int, recordMode bool, bitDepth int, numPBOs int, ad audio.AudioDevice, ctx graphics.Context) (*Renderer, error) {
	r := &Renderer{
		width:       width,
		height:      height,
		recordMode:  recordMode,
		audioDevice: ad,
		context:     ctx,
	}
	var err error

	r.namedPasses = make(map[string]*RenderPass)
	r.bufferPasses = make([]*RenderPass, 0)
	r.buffers = make(map[string]*inputs.Buffer)

	// Make the context current on this thread.
	r.context.MakeCurrent()

	// Use the sync.Once to safely initialize OpenGL bindings.
	var initErr error
	glInitOnce.Do(func() {
		initErr = gl.Init()
	})
	if initErr != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", initErr)
	}

	r.offscreenRenderer, err = NewOffscreenRenderer(r.width, r.height, bitDepth, numPBOs)
	if err != nil {
		return nil, fmt.Errorf("failed to create offscreen renderer: %w", err)
	}

	return r, nil
}

func (r *Renderer) Shutdown() {
	// The context itself will be shut down by the manager
	for _, pass := range r.namedPasses {
		gl.DeleteProgram(pass.ShaderProgram)
	}
	for _, buffer := range r.buffers {
		buffer.Destroy()
	}
	for _, pass := range r.namedPasses {
		for _, ch := range pass.Channels {
			if ch != nil {
				if _, ok := ch.(*inputs.Buffer); !ok {
					ch.Destroy()
				}
			}
		}
	}
	gl.DeleteProgram(r.blitProgram)
	gl.DeleteProgram(r.yuvProgram)
	if r.offscreenRenderer != nil {
		r.offscreenRenderer.Destroy()
	}
	gl.DeleteVertexArrays(1, &r.quadVAO)
}
