//go:build !linux

package renderer

import (
	"fmt"
	"sync" // Import the sync package

	gl "github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/audio"
	"github.com/richinsley/goshadertoy/graphics"
	shader "github.com/richinsley/goshadertoy/shader"
)

// Add the same sync.Once variable here.
var glInitOnce sync.Once

// Renderer struct for non-Linux platforms.
type Renderer struct {
	context graphics.Context
	quadVAO uint32

	// the current scene to render
	activeScene *Scene

	// Renderer-specific resources remain
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

	// Make the context current BEFORE initializing OpenGL bindings for this thread.
	r.context.MakeCurrent()

	// Initialize the OpenGL function pointers once per application run.
	var initErr error
	glInitOnce.Do(func() {
		initErr = gl.Init()
	})
	if initErr != nil {
		return nil, fmt.Errorf("failed to initialize OpenGL: %w", initErr)
	}

	// Create Renderer-Specific (not Scene-Specific) Resources

	// Create a shared Vertex Array Object for drawing quads
	var vbo uint32
	gl.GenVertexArrays(1, &r.quadVAO)
	gl.GenBuffers(1, &vbo)
	gl.BindVertexArray(r.quadVAO)
	gl.BindBuffer(gl.ARRAY_BUFFER, vbo)
	gl.BufferData(gl.ARRAY_BUFFER, len(quadVertices)*4, gl.Ptr(quadVertices), gl.STATIC_DRAW)
	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointer(0, 2, gl.FLOAT, false, 2*4, gl.PtrOffset(0))
	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	gl.BindVertexArray(0)

	// Compile utility shaders
	isGLES := r.context.IsGLES()
	blitVertexSource := shader.GenerateVertexShader(isGLES)
	blitFragmentSource := shader.GetBlitFragmentShader(r.recordMode, isGLES)
	yuvFragmentSource := shader.GetYUVFragmentShader(isGLES)

	var err error
	r.blitProgram, err = newProgram(blitVertexSource, blitFragmentSource)
	if err != nil {
		return nil, fmt.Errorf("failed to create blit program: %w", err)
	}

	r.yuvProgram, err = newProgram(blitVertexSource, yuvFragmentSource)
	if err != nil {
		return nil, fmt.Errorf("failed to create yuv program: %w", err)
	}
	r.yuvBitDepthLoc = gl.GetUniformLocation(r.yuvProgram, gl.Str("u_bitDepth\x00"))

	// Initialize the offscreen renderer for recording/streaming
	r.offscreenRenderer, err = NewOffscreenRenderer(r.width, r.height, bitDepth, numPBOs)
	if err != nil {
		return nil, fmt.Errorf("failed to create offscreen renderer: %w", err)
	}

	return r, nil
}

func (r *Renderer) Shutdown() {
	// The renderer is only responsible for its own resources and the active scene.

	// Delegate scene-specific cleanup to the scene itself.
	if r.activeScene != nil {
		r.activeScene.Destroy()
		r.activeScene = nil
	}

	// Clean up renderer-specific resources.
	gl.DeleteProgram(r.blitProgram)
	gl.DeleteProgram(r.yuvProgram)
	if r.offscreenRenderer != nil {
		r.offscreenRenderer.Destroy()
	}
	gl.DeleteVertexArrays(1, &r.quadVAO)

	// The context itself is managed and shut down by the main application.
}
