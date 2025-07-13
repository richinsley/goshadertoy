//go:build linux

package renderer

import (
	"fmt"
	"runtime"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	glfwcontext "github.com/richinsley/goshadertoy/glfwcontext"
	headless "github.com/richinsley/goshadertoy/headless"
	inputs "github.com/richinsley/goshadertoy/inputs"
)

// Renderer struct for Linux, includes headless context.
type Renderer struct {
	context           *glfwcontext.Context
	headlessContext   *headless.Headless
	quadVAO           uint32
	bufferPasses      []*RenderPass
	namedPasses       map[string]*RenderPass
	buffers           map[string]*inputs.Buffer
	offscreenRenderer *OffscreenRenderer
	blitProgram       uint32
	width             int
	height            int
	recordMode        bool
}

func NewRenderer(width, height int, visible bool, bitDepth int, numPBOs int) (*Renderer, error) {
	r := &Renderer{
		width:      width,
		height:     height,
		recordMode: !visible,
	}
	var err error

	r.namedPasses = make(map[string]*RenderPass)
	r.bufferPasses = make([]*RenderPass, 0)
	r.buffers = make(map[string]*inputs.Buffer)

	if r.recordMode && runtime.GOOS == "linux" {
		r.headlessContext, err = headless.NewHeadless(width, height)
	} else {
		r.context, err = glfwcontext.New(width, height, visible)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to initialize graphics context: %w", err)
	}

	r.offscreenRenderer, err = NewOffscreenRenderer(r.width, r.height, bitDepth, numPBOs)
	if err != nil {
		return nil, fmt.Errorf("failed to create offscreen renderer: %w", err)
	}

	return r, nil
}

func (r *Renderer) Shutdown() {
	for _, pass := range r.namedPasses {
		gl.DeleteProgram(pass.shaderProgram)
	}
	for _, buffer := range r.buffers {
		buffer.Destroy()
	}
	for _, pass := range r.namedPasses {
		for _, ch := range pass.channels {
			if ch != nil {
				if _, ok := ch.(*inputs.Buffer); !ok {
					ch.Destroy()
				}
			}
		}
	}
	gl.DeleteProgram(r.blitProgram)
	if r.offscreenRenderer != nil {
		r.offscreenRenderer.Destroy()
	}
	gl.DeleteVertexArrays(1, &r.quadVAO)
	if r.context != nil {
		r.context.Shutdown()
	}
	if r.headlessContext != nil {
		r.headlessContext.Shutdown()
	}
}