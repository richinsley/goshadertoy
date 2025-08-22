package inputs

import (
	"fmt"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
)

// Buffer manages two sets of FBOs and textures for double-buffering.
// This allows for effects where a shader pass reads from the output of the previous frame.
type Buffer struct {
	ctype string

	// Double-buffering resources
	fbo        [2]uint32
	textureID  [2]uint32
	readIndex  int // Index of the texture to be read from (the result of the previous frame)
	writeIndex int // Index of the FBO to write to (the current frame)

	resolution [3]float32

	// Render pass specific state that will be set by the renderer
	ShaderProgram uint32
	PassInputs    []IChannel
	QuadVAO       uint32
	wrap          string
	filter        string
}

// NewBuffer creates the necessary OpenGL resources for a render buffer.
// It initializes two framebuffers and two textures for double buffering.
func NewBuffer(width, height int, vao uint32) (*Buffer, error) {
	b := &Buffer{
		ctype:      "buffer",
		QuadVAO:    vao,
		readIndex:  0,
		writeIndex: 1,
		wrap:       "clamp",
		filter:     "linear",
	}

	for i := 0; i < 2; i++ {
		var fbo, texture uint32
		gl.GenTextures(1, &texture)
		gl.BindTexture(gl.TEXTURE_2D, texture)
		// Use a floating-point texture format to allow for high dynamic range rendering.
		gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)

		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

		gl.GenFramebuffers(1, &fbo)
		gl.BindFramebuffer(gl.FRAMEBUFFER, fbo)
		// Attach the texture to the FBO
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0)

		if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
			return nil, fmt.Errorf("framebuffer %d for buffer is not complete", i)
		}

		b.fbo[i] = fbo
		b.textureID[i] = texture
	}

	// Unbind to avoid accidental modifications
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	gl.BindTexture(gl.TEXTURE_2D, 0)

	b.resolution = [3]float32{float32(width), float32(height), 1.0}
	return b, nil
}

// BindForWriting binds the current write-target FBO.
func (b *Buffer) BindForWriting() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, b.fbo[b.writeIndex])
}

// UnbindForWriting unbinds the FBO.
func (b *Buffer) UnbindForWriting() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

// SwapBuffers toggles the read/write indices. This is called after the buffer has been rendered to.
func (b *Buffer) SwapBuffers() {
	b.readIndex, b.writeIndex = b.writeIndex, b.readIndex
}

// GetTextureID returns the ID of the texture that should be read from (the result of the previous frame).
func (b *Buffer) GetTextureID() uint32 {
	return b.textureID[b.readIndex]
}

// Resize changes the size of both textures and their FBO attachments.
func (b *Buffer) Resize(width, height int) {
	if width == int(b.resolution[0]) && height == int(b.resolution[1]) {
		// No change in size, nothing to do
		return
	}

	// Delete old textures and FBOs
	b.resolution = [3]float32{float32(width), float32(height), 1.0}
	for i := 0; i < 2; i++ {
		gl.BindTexture(gl.TEXTURE_2D, b.textureID[i])
		gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
	}
	gl.BindTexture(gl.TEXTURE_2D, 0)
}

// Method to update texture parameters for both textures in the buffer
func (b *Buffer) UpdateTextureParameters(wrap, filter string, sampler api.Sampler) {
	// Only proceed if there's an actual change.
	if wrap == b.wrap && filter == b.filter {
		return
	}

	minFilter, magFilter := getFilterMode(sampler.Filter)
	wrapmode := getWrapMode(sampler.Wrap)

	for i := 0; i < 2; i++ {
		gl.BindTexture(gl.TEXTURE_2D, b.textureID[i])
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, minFilter)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, magFilter)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, wrapmode)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, wrapmode)

		// We must explicitly generate the mipmaps for the buffer texture.
		if sampler.Filter == "mipmap" {
			gl.GenerateMipmap(gl.TEXTURE_2D)
		}
	}
	// Unbind to clean up
	gl.BindTexture(gl.TEXTURE_2D, 0)

	// Update the buffer's stored parameters after successfully applying them.
	b.wrap = sampler.Wrap
	b.filter = sampler.Filter
}

// IChannel Interface Implementation
func (b *Buffer) GetCType() string          { return b.ctype }
func (b *Buffer) Update(uniforms *Uniforms) { /* The renderer will handle updating buffers */ }
func (b *Buffer) ChannelRes() [3]float32    { return b.resolution }
func (b *Buffer) GetSamplerType() string    { return "sampler2D" }
func (b *Buffer) Destroy() {
	gl.DeleteFramebuffers(2, &b.fbo[0])
	gl.DeleteTextures(2, &b.textureID[0])
	if b.ShaderProgram != 0 {
		gl.DeleteProgram(b.ShaderProgram)
	}
}
