package renderer

import (
	"context"
	"fmt"
	"strings"
	"time"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
	inputs "github.com/richinsley/goshadertoy/inputs"
	shader "github.com/richinsley/goshadertoy/shader"
	gst "github.com/richinsley/goshadertranslator"
)

var translator *gst.ShaderTranslator

type RenderPass struct {
	shaderProgram         uint32
	channels              []inputs.IChannel
	buffer                *inputs.Buffer
	resolutionLoc         int32
	timeLoc               int32
	mouseLoc              int32
	frameLoc              int32
	iChannelLoc           [4]int32
	iChannelResolutionLoc int32
	iDateLoc              int32
	iSampleRateLoc        int32
	iTimeDeltaLoc         int32
	iFrameRateLoc         int32
	iChannelTimeLoc       int32
}

func (r *Renderer) isGLES() bool {
	// isGLES implicitly handled by which files are compiled.
	// We can check if context is nil as a proxy.
	// On Linux, context is nil in headless mode.
	// On other platforms, context is never nil.
	return r.context == nil
}

func (r *Renderer) GetRenderPass(name string, shaderArgs *api.ShaderArgs) (*RenderPass, error) {
	if name == "" {
		name = "image"
	}

	var passArgs *api.BufferRenderPass
	var exists bool
	if passArgs, exists = shaderArgs.Buffers[name]; !exists {
		return nil, fmt.Errorf("no render pass found with name: %s", name)
	}

	width, height := r.width, r.height
	if r.context != nil {
		width, height = r.context.GetFramebufferSize()
	}

	channels, err := inputs.GetChannels(passArgs.Inputs, width, height, r.quadVAO, r.buffers)
	if err != nil {
		return nil, fmt.Errorf("failed to create channels: %w", err)
	}

	fullFragmentSource := shader.GetFragmentShader(channels, shaderArgs.CommonCode, passArgs.Code)

	outputFormat := gst.OutputFormatGLSL330
	if r.isGLES() {
		outputFormat = gst.OutputFormatESSL
	}

	fsShader, err := translator.TranslateShader(fullFragmentSource, "fragment", gst.ShaderSpecWebGL2, outputFormat)
	if err != nil {
		return nil, fmt.Errorf("fragment shader translation failed: %w", err)
	}

	retv := &RenderPass{
		shaderProgram: 0,
		channels:      channels,
	}
	if name != "image" {
		retv.buffer = r.buffers[name]
	}

	vertexShaderSource := shader.GenerateVertexShader(r.isGLES())

	retv.shaderProgram, err = newProgram(vertexShaderSource, fsShader.Code)
	if err != nil {
		return nil, fmt.Errorf("failed to create shader program: %w", err)
	}

	uniformMap := fsShader.Variables
	gl.UseProgram(retv.shaderProgram)

	retv.resolutionLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iResolution")
	retv.timeLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iTime")
	retv.mouseLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iMouse")
	retv.frameLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iFrame")
	retv.iDateLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iDate")
	retv.iSampleRateLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iSampleRate")
	retv.iTimeDeltaLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iTimeDelta")
	retv.iFrameRateLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iFrameRate")

	retv.iChannelTimeLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iChannelTime[0]")
	if retv.iChannelTimeLoc < 0 {
		retv.iChannelTimeLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iChannelTime")
	}

	retv.iChannelResolutionLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iChannelResolution[0]")
	if retv.iChannelResolutionLoc < 0 {
		retv.iChannelResolutionLoc = r.GetUniformLocation(uniformMap, retv.shaderProgram, "iChannelResolution")
	}

	for i := 0; i < 4; i++ {
		samplerName := fmt.Sprintf("iChannel%d", i)
		retv.iChannelLoc[i] = -1
		if v, ok := uniformMap[samplerName]; ok {
			retv.iChannelLoc[i] = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
		}
	}

	return retv, nil
}

func (r *Renderer) InitScene(shaderArgs *api.ShaderArgs) error {
	var err error
	if translator == nil {
		ctx := context.Background()
		translator, err = gst.NewShaderTranslator(ctx)
		if err != nil {
			return err
		}
	}
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

	blitVertexSource := shader.GenerateVertexShader(r.isGLES())
	blitFragmentSource := shader.GetBlitFragmentShader(r.recordMode, r.isGLES())

	r.blitProgram, err = newProgram(blitVertexSource, blitFragmentSource)
	if err != nil {
		return fmt.Errorf("failed to create blit program: %w", err)
	}

	width, height := r.width, r.height
	if !r.recordMode && r.context != nil {
		width, height = r.context.GetFramebufferSize()
	}

	for _, name := range []string{"A", "B", "C", "D"} {
		if _, exists := shaderArgs.Buffers[name]; exists {
			buffer, err := inputs.NewBuffer(width, height, r.quadVAO)
			if err != nil {
				return fmt.Errorf("failed to create buffer %s: %w", name, err)
			}
			r.buffers[name] = buffer
		}
	}

	passnames := []string{"A", "B", "C", "D", "image"}
	for _, name := range passnames {
		pass, err := r.GetRenderPass(name, shaderArgs)
		if err != nil {
			if strings.HasPrefix(err.Error(), "no render pass found with name") {
				continue
			}
			return fmt.Errorf("failed to create render pass %s: %v", name, err)
		}
		r.namedPasses[name] = pass
		if name != "image" {
			r.bufferPasses = append(r.bufferPasses, pass)
		}
	}
	return nil
}

var quadVertices = []float32{
	-1.0, 1.0, -1.0, -1.0, 1.0, -1.0,
	-1.0, 1.0, 1.0, -1.0, 1.0, 1.0,
}

func (r *Renderer) GetUniformLocation(uniformMap map[string]gst.ShaderVariable, shaderProgram uint32, name string) int32 {
	if v, ok := uniformMap[name]; ok {
		loc := gl.GetUniformLocation(shaderProgram, gl.Str(v.MappedName+"\x00"))
		if loc < 0 {
			return -1
		}
		return loc
	}
	return -1
}

func (r *Renderer) RenderFrame(time float64, frameCount int32, mouseData [4]float32, uniforms *inputs.Uniforms) {
	var renderWidth, renderHeight int

	if r.recordMode {
		renderWidth = r.width
		renderHeight = r.height
	} else if r.context != nil {
		fbWidth, fbHeight := r.context.GetFramebufferSize()
		renderWidth = fbWidth
		renderHeight = fbHeight

		if fbWidth != r.offscreenRenderer.width || fbHeight != r.offscreenRenderer.height {
			r.offscreenRenderer.width = fbWidth
			r.offscreenRenderer.height = fbHeight
			gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
			gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, int32(fbWidth), int32(fbHeight), 0, gl.RGBA, gl.UNSIGNED_BYTE, nil)

			gl.BindRenderbuffer(gl.RENDERBUFFER, r.offscreenRenderer.depthRenderbuffer)
			gl.RenderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT16, int32(fbWidth), int32(fbHeight))

			for _, buffer := range r.buffers {
				buffer.Resize(fbWidth, fbHeight)
			}
		}
	} else {
		renderWidth = r.width
		renderHeight = r.height
	}

	for _, pass := range r.bufferPasses {
		if pass.buffer != nil {
			pass.buffer.BindForWriting()
		}

		gl.UseProgram(pass.shaderProgram)
		updateUniforms(pass, renderWidth, renderHeight, uniforms)
		bindChannels(pass, uniforms)
		gl.Viewport(0, 0, int32(renderWidth), int32(renderHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)
		unbindChannels(pass)

		if pass.buffer != nil {
			pass.buffer.UnbindForWriting()
			pass.buffer.SwapBuffers()
		}
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, r.offscreenRenderer.fbo)
	imagePass := r.namedPasses["image"]
	gl.UseProgram(imagePass.shaderProgram)
	updateUniforms(imagePass, renderWidth, renderHeight, uniforms)
	bindChannels(imagePass, uniforms)
	gl.Viewport(0, 0, int32(renderWidth), int32(renderHeight))
	gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
	gl.BindVertexArray(r.quadVAO)
	gl.DrawArrays(gl.TRIANGLES, 0, 6)
	unbindChannels(imagePass)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

func (r *Renderer) Run() {
	if r.context == nil {
		return
	}
	startTime := r.context.Time()
	var frameCount int32 = 0
	var lastMouseClickX, lastMouseClickY float64
	var mouseWasDown bool
	var lastFrameTime = r.context.Time()

	for !r.context.ShouldClose() {
		currentTime := r.context.Time() - startTime
		timeDelta := float32(currentTime - lastFrameTime)
		lastFrameTime = currentTime

		var mouseData [4]float32
		win := r.context.Window()
		if win != nil {
			fbWidth, fbHeight := r.context.GetFramebufferSize()
			winWidth, winHeight := win.GetSize()
			var scaleX, scaleY float64 = 1.0, 1.0
			if winWidth > 0 && winHeight > 0 {
				scaleX = float64(fbWidth) / float64(winWidth)
				scaleY = float64(fbHeight) / float64(winHeight)
			}
			cursorX, cursorY := win.GetCursorPos()
			pixelX := cursorX * scaleX
			pixelY := cursorY * scaleY
			mouseX := float32(pixelX)
			mouseY := float32(fbHeight) - float32(pixelY)
			const mouseLeft = 0
			isMouseDown := win.GetMouseButton(mouseLeft) == 1
			if isMouseDown && !mouseWasDown {
				lastMouseClickX = pixelX
				lastMouseClickY = pixelY
			}
			mouseWasDown = isMouseDown
			clickX := float32(lastMouseClickX)
			clickY := float32(fbHeight) - float32(lastMouseClickY)
			if !isMouseDown {
				clickX = -clickX
				clickY = -clickY
			}
			mouseData = [4]float32{mouseX, mouseY, clickX, clickY}
		}

		var sampleRate float32 = 44100
		var channelResolutions [4][3]float32
		if imagePass, ok := r.namedPasses["image"]; ok {
			for i, ch := range imagePass.channels {
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

		r.RenderFrame(currentTime, frameCount, mouseData, uniforms)

		fbWidth, fbHeight := r.context.GetFramebufferSize()
		gl.Viewport(0, 0, int32(fbWidth), int32(fbHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
		gl.UseProgram(r.blitProgram)
		gl.ActiveTexture(gl.TEXTURE0)
		gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)
		gl.BindTexture(gl.TEXTURE_2D, 0)

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
	for chIndex, ch := range pass.channels {
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
	for chIndex, ch := range pass.channels {
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
