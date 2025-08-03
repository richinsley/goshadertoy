package renderer

import (
	"context"
	"fmt"
	"log"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/api"
	"github.com/richinsley/goshadertoy/graphics"
	inputs "github.com/richinsley/goshadertoy/inputs"
	options "github.com/richinsley/goshadertoy/options"
	"github.com/richinsley/goshadertoy/shader"
	xlate "github.com/richinsley/goshadertoy/translator"
	gst "github.com/richinsley/goshadertranslator"
)

const (
	soundTextureWidth  = 512
	soundTextureHeight = 512
	soundSampleRate    = 44100
)

// SoundShaderRenderer manages the execution of a sound shader.
type SoundShaderRenderer struct {
	context         graphics.Context
	program         uint32
	fbo             uint32
	textureID       uint32
	quadVAO         uint32
	preRenderedChan chan<- []float32
	shaderArgs      *api.ShaderArgs
	options         *options.ShaderOptions
	uniformMap      map[string]gst.ShaderVariable
	channels        []inputs.IChannel

	// uniform locations to match the official spec
	timeOffsetLoc        int32
	sampleOffsetLoc      int32
	sampleRateLoc        int32
	dateLoc              int32
	channelTimeLoc       int32
	channelResolutionLoc int32
	iChannelLoc          [4]int32
}

func (ssr *SoundShaderRenderer) GetUniformLocation(name string) int32 {
	if v, ok := ssr.uniformMap[name]; ok {
		// Use the MappedName provided by the translator
		return gl.GetUniformLocation(ssr.program, gl.Str(v.MappedName+"\x00"))
	}
	return -1
}

// NewSoundShaderRenderer creates a new renderer for sound shaders.
func NewSoundShaderRenderer(ctx graphics.Context, preRenderedChan chan<- []float32, shaderArgs *api.ShaderArgs, options *options.ShaderOptions) *SoundShaderRenderer {
	return &SoundShaderRenderer{
		context:         ctx,
		preRenderedChan: preRenderedChan,
		shaderArgs:      shaderArgs,
		options:         options,
	}
}

// InitGL sets up all OpenGL resources. It MUST be called from the goroutine
// that will be running the rendering loop, after locking the OS thread.
func (ssr *SoundShaderRenderer) InitGL() error {
	// This function now assumes it's running on the correct, locked thread.
	ssr.context.MakeCurrent()
	defer ssr.context.DetachCurrent() // Detach context when done with setup

	// Initialize OpenGL bindings for this context
	var initErr error
	glInitOnce.Do(func() {
		initErr = gl.Init()
	})
	if initErr != nil {
		return fmt.Errorf("sound renderer gl.Init failed: %w", initErr)
	}

	passArgs := ssr.shaderArgs.Buffers["sound"]
	if passArgs == nil {
		return fmt.Errorf("no sound shader found in shader arguments")
	}

	// Setup VAO
	var vbo uint32
	gl.GenVertexArrays(1, &ssr.quadVAO)
	gl.GenBuffers(1, &vbo)
	gl.BindVertexArray(ssr.quadVAO)
	gl.BindBuffer(gl.ARRAY_BUFFER, vbo)
	gl.BufferData(gl.ARRAY_BUFFER, len(quadVertices)*4, gl.Ptr(quadVertices), gl.STATIC_DRAW)
	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointer(0, 2, gl.FLOAT, false, 2*4, gl.PtrOffset(0))
	gl.BindVertexArray(0)

	// Setup FBO and Texture
	gl.GenFramebuffers(1, &ssr.fbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, ssr.fbo)
	gl.GenTextures(1, &ssr.textureID)
	gl.BindTexture(gl.TEXTURE_2D, ssr.textureID)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, soundTextureWidth, soundTextureHeight, 0, gl.RGBA, gl.UNSIGNED_BYTE, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, ssr.textureID, 0)
	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return fmt.Errorf("sound renderer FBO is not complete")
	}
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)

	// Compile Shader
	vertexShaderSource := shader.GenerateVertexShader(ssr.context.IsGLES())

	commoncode := ""
	if commonPass, ok := ssr.shaderArgs.Buffers["common"]; ok {
		commoncode = commonPass.Code
	}

	var err error
	ssr.channels, err = inputs.GetChannels(passArgs.Inputs, soundTextureWidth, soundTextureHeight, ssr.quadVAO, nil, ssr.options, nil)
	if err != nil {
		return fmt.Errorf("failed to create channels for sound shader: %w", err)
	}

	fullFragmentSource := shader.GenerateSoundShaderSource(commoncode, passArgs.Code, ssr.channels)

	outputFormat := gst.OutputFormatGLSL330
	if ssr.context.IsGLES() {
		outputFormat = gst.OutputFormatESSL
	}

	translator := xlate.GetTranslator()
	fsShader, err := translator.TranslateShader(fullFragmentSource, "fragment", gst.ShaderSpecWebGL2, outputFormat)
	if err != nil {
		fmt.Printf("Problematic Sound Shader Source:\n%s\n", fullFragmentSource)
		return fmt.Errorf("sound shader translation failed: %w", err)
	}

	// Store the uniform map for later use
	ssr.uniformMap = fsShader.Variables

	ssr.program, err = newProgram(vertexShaderSource, fsShader.Code)
	if err != nil {
		return fmt.Errorf("failed to create sound shader program: %w", err)
	}

	if ssr.program == 0 {
		return fmt.Errorf("sound shader program failed to link, resulting in ID 0")
	}

	gl.UseProgram(ssr.program)

	// Get Uniforms and verify they are valid, matching the official spec.
	ssr.timeOffsetLoc = ssr.GetUniformLocation("iTimeOffset")
	ssr.sampleOffsetLoc = ssr.GetUniformLocation("iSampleOffset")
	ssr.sampleRateLoc = ssr.GetUniformLocation("iSampleRate")
	ssr.dateLoc = ssr.GetUniformLocation("iDate")
	ssr.channelTimeLoc = ssr.GetUniformLocation("iChannelTime")
	ssr.channelResolutionLoc = ssr.GetUniformLocation("iChannelResolution")

	// Get iChannelN sampler locations
	for i := 0; i < 4; i++ {
		samplerName := fmt.Sprintf("iChannel%d", i)
		ssr.iChannelLoc[i] = ssr.GetUniformLocation(samplerName)
	}

	log.Printf("Sound Shader Uniforms: iTimeOffset=%d, iSampleOffset=%d, iSampleRate=%d, iDate=%d, iChannelTime=%d, iChannelResolution=%d",
		ssr.timeOffsetLoc, ssr.sampleOffsetLoc, ssr.sampleRateLoc, ssr.dateLoc, ssr.channelTimeLoc, ssr.channelResolutionLoc)

	// A check for the most critical uniforms
	if ssr.timeOffsetLoc == -1 || ssr.sampleRateLoc == -1 || ssr.sampleOffsetLoc == -1 {
		log.Println("WARNING: A critical sound shader uniform (time/sample offset/rate) was not found. This will result in silent output.")
	}

	log.Println("Sound Shader Renderer initialized successfully on its dedicated thread.")
	return nil
}

// Run starts the rendering loop for the sound shader.
func (ssr *SoundShaderRenderer) Run(ctx context.Context) {
	ssr.context.MakeCurrent()
	defer ssr.Shutdown()

	var timeOffset float32 = 0.0
	var sampleOffset int32 = 0
	samplesPerFullBuffer := int32(soundTextureWidth * soundTextureHeight)
	timeStepPerFullBuffer := float32(samplesPerFullBuffer) / float32(soundSampleRate)

	for {
		// Check for cancellation at the start of each large render cycle.
		select {
		case <-ctx.Done():
			log.Println("Stopping sound shader renderer.")
			return
		default:
			// Continue to render the next large buffer.
		}

		// --- Render one large buffer ---
		gl.BindFramebuffer(gl.FRAMEBUFFER, ssr.fbo)
		gl.UseProgram(ssr.program)

		// Set uniforms for the start of this buffer
		gl.Uniform1f(ssr.timeOffsetLoc, timeOffset)
		gl.Uniform1i(ssr.sampleOffsetLoc, sampleOffset)
		gl.Uniform1f(ssr.sampleRateLoc, soundSampleRate)
		// log.Println("Rendering sound shader frame at timeOffset:", timeOffset, "sampleOffset:", sampleOffset)

		gl.Viewport(0, 0, soundTextureWidth, soundTextureHeight)
		gl.BindVertexArray(ssr.quadVAO)

		bindChannelsSound(ssr, timeOffset)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)
		unbindChannelsSound(ssr)

		// --- Read the entire large buffer back ---
		pixelData := make([]byte, samplesPerFullBuffer*4)
		gl.ReadBuffer(gl.COLOR_ATTACHMENT0)
		gl.ReadPixels(0, 0, soundTextureWidth, soundTextureHeight, gl.RGBA, gl.UNSIGNED_BYTE, gl.Ptr(pixelData))
		gl.BindFramebuffer(gl.FRAMEBUFFER, 0)

		// Convert and send the entire buffer in one go.
		audioSamples := ssr.convertPixelsToAudio(pixelData)

		select {
		case ssr.preRenderedChan <- audioSamples:
			// Successfully sent the buffer.
		case <-ctx.Done():
			log.Println("Stopping sound shader renderer during send.")
			return
		}

		// Increment offsets for the next large buffer
		timeOffset += timeStepPerFullBuffer
		sampleOffset += samplesPerFullBuffer
	}
}

// Shutdown cleans up the OpenGL resources.
func (ssr *SoundShaderRenderer) Shutdown() {
	gl.DeleteProgram(ssr.program)
	gl.DeleteFramebuffers(1, &ssr.fbo)
	gl.DeleteTextures(1, &ssr.textureID)
	gl.DeleteVertexArrays(1, &ssr.quadVAO)
	log.Println("Sound Shader Renderer resources cleaned up.")
}

// convertPixelsToAudio decodes RGBA8 pixels into stereo float32 audio samples.
// Shadertoy encodes 16-bit audio into two 8-bit channels (e.g., R and G).
func (ssr *SoundShaderRenderer) convertPixelsToAudio(pixels []byte) []float32 {
	numSamples := len(pixels) / 4 // Each pixel is one stereo sample
	samples := make([]float32, numSamples*2)

	for i := 0; i < numSamples; i++ {
		// Left channel is encoded in R (low byte) and G (high byte)
		leftLow := float32(pixels[i*4+0])
		leftHigh := float32(pixels[i*4+1])
		leftVal := (leftLow + leftHigh*256.0) / 65535.0 // Combine and normalize to [0, 1]
		samples[i*2] = leftVal*2.0 - 1.0                // Convert to [-1, 1]

		// Right channel is encoded in B (low byte) and A (high byte)
		rightLow := float32(pixels[i*4+2])
		rightHigh := float32(pixels[i*4+3])
		rightVal := (rightLow + rightHigh*256.0) / 65535.0 // Combine and normalize to [0, 1]
		samples[i*2+1] = rightVal*2.0 - 1.0                // Convert to [-1, 1]
	}
	return samples
}

func bindChannelsSound(ssr *SoundShaderRenderer, time float32) {
	for i, ch := range ssr.channels {
		if ch == nil {
			continue
		}
		// Create a dummy Uniforms struct for channel updates
		uniforms := &inputs.Uniforms{Time: time}
		ch.Update(uniforms)

		var texTarget uint32
		switch ch.GetSamplerType() {
		case "sampler3D":
			texTarget = gl.TEXTURE_3D
		case "samplerCube":
			texTarget = gl.TEXTURE_CUBE_MAP
		default:
			texTarget = gl.TEXTURE_2D
		}

		if ssr.iChannelLoc[i] != -1 {
			gl.ActiveTexture(gl.TEXTURE0 + uint32(i))
			gl.BindTexture(texTarget, ch.GetTextureID())
			gl.Uniform1i(ssr.iChannelLoc[i], int32(i))
		}
	}
}

func unbindChannelsSound(ssr *SoundShaderRenderer) {
	for i, ch := range ssr.channels {
		if ch != nil {
			var texTarget uint32
			switch ch.GetSamplerType() {
			case "sampler3D":
				texTarget = gl.TEXTURE_3D
			case "samplerCube":
				texTarget = gl.TEXTURE_CUBE_MAP
			default:
				texTarget = gl.TEXTURE_2D
			}
			gl.ActiveTexture(gl.TEXTURE0 + uint32(i))
			gl.BindTexture(texTarget, 0)
		}
	}
}
