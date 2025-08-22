package renderer

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	audio "github.com/richinsley/goshadertoy/audio"
	glfwcontext "github.com/richinsley/goshadertoy/glfwcontext"
	inputs "github.com/richinsley/goshadertoy/inputs"
	gst "github.com/richinsley/goshadertranslator"
)

func (r *Renderer) isGLES() bool {
	// In record mode on Linux, we use a headless EGL context which uses GLES.
	// For all other cases (interactive mode or other OSes), we use GLFW with desktop GL.
	return r.recordMode && runtime.GOOS == "linux"
}

var quadVertices = []float32{
	-1.0, 1.0, -1.0, -1.0, 1.0, -1.0,
	-1.0, 1.0, 1.0, -1.0, 1.0, 1.0,
}

func (r *Renderer) GetUniformLocation(uniformMap map[string]gst.ShaderVariable, ShaderProgram uint32, name string) int32 {
	if v, ok := uniformMap[name]; ok {
		loc := gl.GetUniformLocation(ShaderProgram, gl.Str(v.MappedName+"\x00"))
		if loc < 0 {
			return -1
		}
		return loc
	}
	return -1
}

// SetScene allows switching the active scene. It returns the previously active scene
// so the caller can choose to destroy it.
func (r *Renderer) SetScene(scene *Scene) *Scene {
	previousScene := r.activeScene
	r.activeScene = scene
	if scene != nil {
		log.Printf("Renderer active scene set to: %s", scene.Title)
	}
	return previousScene
}

func (r *Renderer) RenderFrame(uniforms *inputs.Uniforms) {
	if r.activeScene == nil {
		return // Can't render without a scene
	}

	var renderWidth, renderHeight int

	if r.recordMode {
		renderWidth = r.width
		renderHeight = r.height
	} else if r.context != nil {
		fbWidth, fbHeight := r.context.GetFramebufferSize()
		renderWidth = fbWidth
		renderHeight = fbHeight

		// Check if the framebuffer size has changed
		if fbWidth != r.offscreenRenderer.width || fbHeight != r.offscreenRenderer.height {
			log.Printf("Resizing renderer and scene buffers to %dx%d", fbWidth, fbHeight)

			// Resize the renderer's own FBO
			r.offscreenRenderer.width = fbWidth
			r.offscreenRenderer.height = fbHeight
			gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
			gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA16F, int32(fbWidth), int32(fbHeight), 0, gl.RGBA, gl.FLOAT, nil)
			gl.BindRenderbuffer(gl.RENDERBUFFER, r.offscreenRenderer.depthRenderbuffer)
			gl.RenderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT16, int32(fbWidth), int32(fbHeight))

			// IMPORTANT: Resize the active scene's buffers
			for _, buffer := range r.activeScene.Buffers {
				buffer.Resize(fbWidth, fbHeight)
			}
		}
	} else {
		// Fallback for unexpected configurations
		renderWidth = r.width
		renderHeight = r.height
	}

	// Render Buffer Passes from the Active Scene
	for _, pass := range r.activeScene.BufferPasses {
		if pass.Buffer == nil {
			continue // Should not happen, but a safe check
		}

		pass.Buffer.BindForWriting()

		gl.UseProgram(pass.ShaderProgram)
		updateUniforms(pass, renderWidth, renderHeight, uniforms)
		bindChannels(pass, uniforms)

		gl.Viewport(0, 0, int32(renderWidth), int32(renderHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)

		unbindChannels(pass)
		pass.Buffer.UnbindForWriting()
		pass.Buffer.SwapBuffers()
	}

	// Render the Final Image Pass from the Active Scene
	imagePass := r.activeScene.ImagePass
	if imagePass != nil {
		gl.BindFramebuffer(gl.FRAMEBUFFER, r.offscreenRenderer.fbo)
		gl.UseProgram(imagePass.ShaderProgram)
		updateUniforms(imagePass, renderWidth, renderHeight, uniforms)
		bindChannels(imagePass, uniforms)

		gl.Viewport(0, 0, int32(renderWidth), int32(renderHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)

		unbindChannels(imagePass)
		gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	}
}

func (r *Renderer) RenderToYUV() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
	gl.UseProgram(r.yuvProgram)
	gl.Uniform1i(r.yuvBitDepthLoc, int32(r.offscreenRenderer.bitDepth))
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
	gl.Viewport(0, 0, int32(r.width), int32(r.height))
	gl.Clear(gl.COLOR_BUFFER_BIT)
	gl.BindVertexArray(r.quadVAO)
	gl.DrawArrays(gl.TRIANGLES, 0, 6)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

func (r *Renderer) Run() {
	if r.context == nil {
		return // Cannot run in interactive mode without a window context
	}
	startTime := r.context.Time()
	var frameCount int32 = 0
	var lastFrameTime = r.context.Time()

	for !r.context.ShouldClose() {
		// If no scene is active, just clear the screen and continue.
		if r.activeScene == nil {
			fbWidth, fbHeight := r.context.GetFramebufferSize()
			gl.Viewport(0, 0, int32(fbWidth), int32(fbHeight))
			gl.ClearColor(0.0, 0.0, 0.0, 1.0)
			gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
			r.context.EndFrame()
			continue
		}

		currentTime := r.context.Time() - startTime
		timeDelta := float32(currentTime - lastFrameTime)
		lastFrameTime = currentTime

		mouseData := r.context.GetMouseInput()

		var sampleRate float32 = 44100
		var channelResolutions [4][3]float32
		// Get channel info from the active scene's image pass
		if r.activeScene.ImagePass != nil {
			for i, ch := range r.activeScene.ImagePass.Channels {
				if ch != nil {
					channelResolutions[i] = ch.ChannelRes()
					if mic, ok := ch.(interface{ SampleRate() int }); ok {
						sampleRate = float32(mic.SampleRate())
					}
				}
			}
		}

		frameRate := float32(1.0 / timeDelta)
		if timeDelta == 0 {
			frameRate = 60.0
		}

		uniforms := &inputs.Uniforms{
			Time:              float32(currentTime),
			TimeDelta:         timeDelta,
			FrameRate:         frameRate,
			Frame:             frameCount,
			Mouse:             mouseData,
			ChannelTime:       [4]float32{float32(currentTime), float32(currentTime), float32(currentTime), float32(currentTime)},
			SampleRate:        sampleRate,
			ChannelResolution: channelResolutions,
		}

		// Find the mic channel within the active scene
		micChannel := findMicChannel(r.activeScene)
		if micChannel != nil {
			const fftInputSize = 2048 // From inputs/mic.go
			samples := r.audioDevice.GetBuffer().WindowPeek()
			monoSamples := audio.DownmixStereoToMono(samples)
			micChannel.ProcessAudio(monoSamples)
		}

		r.RenderFrame(uniforms)

		// Blit the final rendered texture to the screen
		if _, ok := r.context.(*glfwcontext.Context); ok {
			fbWidth, fbHeight := r.context.GetFramebufferSize()
			gl.Viewport(0, 0, int32(fbWidth), int32(fbHeight))
			gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
			gl.UseProgram(r.blitProgram)
			gl.ActiveTexture(gl.TEXTURE0)
			gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
			gl.BindVertexArray(r.quadVAO)
			gl.DrawArrays(gl.TRIANGLES, 0, 6)
			gl.BindTexture(gl.TEXTURE_2D, 0)
		}

		r.context.EndFrame()
		frameCount++
	}
}

func updateUniforms(pass *RenderPass, width, height int, uniforms *inputs.Uniforms) {
	if pass.resolutionLoc != -1 {
		gl.Uniform3f(pass.resolutionLoc, float32(width), float32(height), 0)
	}
	if pass.timeLoc != -1 {
		gl.Uniform1f(pass.timeLoc, uniforms.Time)
	}
	if pass.iTimeDeltaLoc != -1 {
		gl.Uniform1f(pass.iTimeDeltaLoc, uniforms.TimeDelta)
	}
	if pass.iFrameRateLoc != -1 {
		gl.Uniform1f(pass.iFrameRateLoc, uniforms.FrameRate)
	}
	if pass.frameLoc != -1 {
		gl.Uniform1i(pass.frameLoc, uniforms.Frame)
	}
	if pass.mouseLoc != -1 {
		gl.Uniform4f(pass.mouseLoc, uniforms.Mouse[0], uniforms.Mouse[1], uniforms.Mouse[2], uniforms.Mouse[3])
	}
	if pass.iDateLoc != -1 {
		now := time.Now()
		year := float32(now.Year())
		month := float32(now.Month())
		day := float32(now.Day())
		timeInSeconds := float32(now.Hour()*3600 + now.Minute()*60 + now.Second())
		gl.Uniform4f(pass.iDateLoc, year, month, day, timeInSeconds)
	}
	if pass.iSampleRateLoc != -1 {
		gl.Uniform1f(pass.iSampleRateLoc, uniforms.SampleRate)
	}

	if pass.iChannelTimeLoc != -1 {
		gl.Uniform1fv(pass.iChannelTimeLoc, 4, &uniforms.ChannelTime[0])
	}

	if pass.iChannelResolutionLoc != -1 {
		var res_flat [12]float32
		for i := 0; i < 4; i++ {
			res_flat[i*3] = uniforms.ChannelResolution[i][0]
			res_flat[i*3+1] = uniforms.ChannelResolution[i][1]
			res_flat[i*3+2] = uniforms.ChannelResolution[i][2]
		}
		gl.Uniform3fv(pass.iChannelResolutionLoc, 4, &res_flat[0])
	}
}

func bindChannels(pass *RenderPass, uniforms *inputs.Uniforms) {
	for chIndex, ch := range pass.Channels {
		if ch == nil {
			continue
		}
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

		if pass.iChannelLoc[chIndex] != -1 {
			gl.ActiveTexture(gl.TEXTURE0 + uint32(chIndex))
			gl.BindTexture(texTarget, ch.GetTextureID())
			gl.Uniform1i(pass.iChannelLoc[chIndex], int32(chIndex))
		}
	}
}

func unbindChannels(pass *RenderPass) {
	for chIndex, ch := range pass.Channels {
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
			gl.ActiveTexture(gl.TEXTURE0 + uint32(chIndex))
			gl.BindTexture(texTarget, 0)
		}
	}
}

func newProgram(vertexShaderSource, fragmentShaderSource string) (uint32, error) {
	vertexShader, err := compileShader(vertexShaderSource, gl.VERTEX_SHADER)
	if err != nil {
		return 0, err
	}
	fragmentShader, err := compileShader(fragmentShaderSource, gl.FRAGMENT_SHADER)
	if err != nil {
		return 0, err
	}

	program := gl.CreateProgram()
	gl.AttachShader(program, vertexShader)
	gl.AttachShader(program, fragmentShader)
	gl.LinkProgram(program)

	var status int32
	gl.GetProgramiv(program, gl.LINK_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetProgramiv(program, gl.INFO_LOG_LENGTH, &logLength)
		log := strings.Repeat("\x00", int(logLength+1))
		gl.GetProgramInfoLog(program, logLength, nil, gl.Str(log))
		return 0, fmt.Errorf("failed to link program: %v", log)
	}

	gl.DeleteShader(vertexShader)
	gl.DeleteShader(fragmentShader)

	return program, nil
}

func compileShader(source string, shaderType uint32) (uint32, error) {
	shader := gl.CreateShader(shaderType)
	csources, free := gl.Strs(source + "\x00")
	gl.ShaderSource(shader, 1, csources, nil)
	free()
	gl.CompileShader(shader)

	var status int32
	gl.GetShaderiv(shader, gl.COMPILE_STATUS, &status)
	if status == gl.FALSE {
		var logLength int32
		gl.GetShaderiv(shader, gl.INFO_LOG_LENGTH, &logLength)
		logText := strings.Repeat("\x00", int(logLength+1))
		gl.GetShaderInfoLog(shader, logLength, nil, gl.Str(logText))
		return 0, fmt.Errorf("failed to compile shader: %v", logText)
	}
	return shader, nil
}
