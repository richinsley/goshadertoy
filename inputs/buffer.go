package inputs

import (
	"fmt"

	"github.com/go-gl/gl/v4.1-core/gl"
)

// Buffer manages two sets of FBOs and textures for double-buffering.  One is the current write target,
// and the other is the read target. It allows for rendering to one texture while reading from another.
// There are instances where a render pass for a buffer will self-reference, meaning it reads from its own output.
type Buffer struct {
	index         int
	ctype         string
	fbo           [2]uint32
	textureID     [2]uint32
	readIndex     int // 0 or 1
	writeIndex    int // 1 or 0
	resolution    [3]float32
	shaderProgram uint32
	passInputs    []IChannel
	quadVAO       uint32
}

// NewBuffer creates the OpenGL resources (FBOs and textures)
func NewBuffer(index int, width, height int, vao uint32) (*Buffer, error) {
	b := &Buffer{
		index:      index,
		ctype:      "buffer",
		quadVAO:    vao,
		readIndex:  0,
		writeIndex: 1,
	}

	for i := 0; i < 2; i++ {
		var fbo, texture uint32
		gl.GenTextures(1, &texture)
		gl.BindTexture(gl.TEXTURE_2D, texture)
		gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

		gl.GenFramebuffers(1, &fbo)
		gl.BindFramebuffer(gl.FRAMEBUFFER, fbo)
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0)

		if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
			return nil, fmt.Errorf("framebuffer %d for buffer %d is not complete", i, index)
		}

		b.fbo[i] = fbo
		b.textureID[i] = texture
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	gl.BindTexture(gl.TEXTURE_2D, 0)

	b.resolution = [3]float32{float32(width), float32(height), 1.0}
	return b, nil
}

// Finalize sets the shader program and inputs for the buffer pass.
func (b *Buffer) Finalize(program uint32, inputs []IChannel) {
	b.shaderProgram = program
	b.passInputs = inputs
}

// Bind for writing to the current write buffer.
func (b *Buffer) Bind() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, b.fbo[b.writeIndex])
}

func (b *Buffer) Unbind() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

// SwapBuffers toggles the read/write indices.
func (b *Buffer) SwapBuffers() {
	b.readIndex, b.writeIndex = b.writeIndex, b.readIndex
}

// GetTextureID returns the ID of the texture that should be read from.
func (b *Buffer) GetTextureID() uint32 {
	return b.textureID[b.readIndex]
}

// Resize changes the size of both textures and their FBO attachments.
func (b *Buffer) Resize(width, height int) {
	b.resolution = [3]float32{float32(width), float32(height), 1.0}
	for i := 0; i < 2; i++ {
		gl.BindTexture(gl.TEXTURE_2D, b.textureID[i])
		gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
	}
	gl.BindTexture(gl.TEXTURE_2D, 0)
}

// --- IChannel Interface Implementation ---
func (b *Buffer) GetInputIndex() int        { return b.index }
func (b *Buffer) GetCType() string          { return b.ctype }
func (b *Buffer) Update(uniforms *Uniforms) { /* Implemented in renderer */ }
func (b *Buffer) ChannelRes() [3]float32    { return b.resolution }
func (b *Buffer) GetSamplerType() string    { return "sampler2D" }
func (b *Buffer) Destroy() {
	gl.DeleteFramebuffers(2, &b.fbo[0])
	gl.DeleteTextures(2, &b.textureID[0])
	if b.shaderProgram != 0 {
		gl.DeleteProgram(b.shaderProgram)
	}
}

// --- Accessor Methods ---

// GetShaderProgram returns the shader program associated with this buffer pass.
func (b *Buffer) GetShaderProgram() uint32 {
	return b.shaderProgram
}

// GetPassInputs returns the slice of IChannel inputs for this buffer pass.
func (b *Buffer) GetPassInputs() []IChannel {
	return b.passInputs
}
