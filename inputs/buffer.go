package inputs

import (
	"fmt"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy"
)

// Buffer represents a render pass target. It manages an FBO and a texture.
type Buffer struct {
	index         int
	ctype         string
	fbo           uint32
	textureID     uint32
	resolution    [3]float32
	sampler       goshadertoy.Sampler
	shaderProgram uint32
	passInputs    []IChannel
}

// NewBuffer creates a new Buffer (FBO and texture).
func NewBuffer(index int, width, height int, sampler goshadertoy.Sampler, program uint32, inputs []IChannel) (*Buffer, error) {
	var fbo, texture uint32

	// Create a texture to render to
	gl.GenTextures(1, &texture)
	gl.BindTexture(gl.TEXTURE_2D, texture)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

	// Create an FBO
	gl.GenFramebuffers(1, &fbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, fbo)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0)

	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("framebuffer for buffer %d is not complete", index)
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	gl.BindTexture(gl.TEXTURE_2D, 0)

	return &Buffer{
		index:         index,
		ctype:         "buffer",
		fbo:           fbo,
		textureID:     texture,
		resolution:    [3]float32{float32(width), float32(height), 1.0},
		sampler:       sampler,
		shaderProgram: program,
		passInputs:    inputs,
	}, nil
}

func (b *Buffer) Bind() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, b.fbo)
}

func (b *Buffer) Unbind() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

// --- IChannel Interface Implementation ---
func (b *Buffer) GetInputIndex() int        { return b.index }
func (b *Buffer) GetCType() string          { return b.ctype }
func (b *Buffer) Update(uniforms *Uniforms) { /* Handled in the main render loop */ }
func (b *Buffer) GetTextureID() uint32      { return b.textureID }
func (b *Buffer) ChannelRes() [3]float32    { return b.resolution }
func (b *Buffer) GetSamplerType() string    { return "sampler2D" }
func (b *Buffer) Destroy() {
	gl.DeleteFramebuffers(1, &b.fbo)
	gl.DeleteTextures(1, &b.textureID)
	gl.DeleteProgram(b.shaderProgram)
}
