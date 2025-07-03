package renderer

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
	"github.com/richinsley/goshadertoy/glfwcontext"
	inputs "github.com/richinsley/goshadertoy/inputs"
	shader "github.com/richinsley/goshadertoy/shader"
	gst "github.com/richinsley/goshadertranslator"
)

var translator *gst.ShaderTranslator

type RenderPass struct {
	shaderProgram         uint32
	channels              []inputs.IChannel
	buffer                *inputs.Buffer // The buffer this pass renders to (if any)
	resolutionLoc         int32
	timeLoc               int32
	mouseLoc              int32
	frameLoc              int32
	iChannelLoc           [4]int32
	iChannelResolutionLoc [4]int32
}

type Renderer struct {
	context           *glfwcontext.Context
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

func NewRenderer(width, height int, visible bool) (*Renderer, error) {
	r := &Renderer{
		width:      width,
		height:     height,
		recordMode: !visible, // Set recordMode based on window visibility
	}
	var err error

	r.namedPasses = make(map[string]*RenderPass)
	r.bufferPasses = make([]*RenderPass, 0)
	r.buffers = make(map[string]*inputs.Buffer)

	r.context, err = glfwcontext.New(width, height, visible)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw context: %w", err)
	}

	// Use the stored width and height to create the offscreen renderer
	r.offscreenRenderer, err = NewOffscreenRenderer(r.width, r.height)
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
	r.offscreenRenderer.Destroy()
	gl.DeleteVertexArrays(1, &r.quadVAO)
	r.context.Shutdown()
}

var quadVertices = []float32{
	-1.0, 1.0, -1.0, -1.0, 1.0, -1.0,
	-1.0, 1.0, 1.0, -1.0, 1.0, 1.0,
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

	width, height := r.context.GetFramebufferSize()
	channels, err := inputs.GetChannels(passArgs.Inputs, width, height, r.quadVAO, r.buffers)
	if err != nil {
		return nil, fmt.Errorf("failed to create channels: %w", err)
	}

	fullFragmentSource := shader.GetFragmentShader(channels, shaderArgs.CommonCode, passArgs.Code)
	fsShader, err := translator.TranslateShader(fullFragmentSource, "fragment", gst.ShaderSpecWebGL2, gst.OutputFormatGLSL330)
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

	vertexShaderSource := shader.GenerateVertexShader()
	retv.shaderProgram, err = newProgram(vertexShaderSource, fsShader.Code)
	if err != nil {
		return nil, fmt.Errorf("failed to create shader program: %w", err)
	}
	retv.channels = channels
	uniformMap := fsShader.Variables
	gl.UseProgram(retv.shaderProgram)

	retv.resolutionLoc = -1
	retv.timeLoc = -1
	retv.mouseLoc = -1
	retv.frameLoc = -1
	if v, ok := uniformMap["iResolution"]; ok {
		retv.resolutionLoc = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}
	if v, ok := uniformMap["iTime"]; ok {
		retv.timeLoc = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}
	if v, ok := uniformMap["iMouse"]; ok {
		retv.mouseLoc = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}
	if v, ok := uniformMap["iFrame"]; ok {
		retv.frameLoc = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}

	for i := 0; i < 4; i++ {
		samplerName := fmt.Sprintf("iChannel%d", i)
		resolutionName := fmt.Sprintf("iChannelResolution[%d]", i)
		retv.iChannelLoc[i] = -1
		retv.iChannelResolutionLoc[i] = -1
		if v, ok := uniformMap[samplerName]; ok {
			retv.iChannelLoc[i] = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
		}
		if v, ok := uniformMap[resolutionName]; ok {
			retv.iChannelResolutionLoc[i] = gl.GetUniformLocation(retv.shaderProgram, gl.Str(v.MappedName+"\x00"))
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

	r.blitProgram, err = newProgram(shader.GenerateVertexShader(), shader.GetBlitFragmentShader())
	if err != nil {
		return fmt.Errorf("failed to create blit program: %w", err)
	}

	width, height := r.context.GetFramebufferSize()
	if r.recordMode {
		width = r.offscreenRenderer.width
		height = r.offscreenRenderer.height
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
			continue
		}
		r.namedPasses[name] = pass
		if name != "image" {
			r.bufferPasses = append(r.bufferPasses, pass)
		}
	}
	return nil
}

func (r *Renderer) RenderFrame(time float64, frameCount int32, mouseData [4]float32) {
	// fbWidth, fbHeight := r.context.GetFramebufferSize()
	// if fbWidth != r.offscreenRenderer.width || fbHeight != r.offscreenRenderer.height {
	// 	for _, buffer := range r.buffers {
	// 		buffer.Resize(fbWidth, fbHeight)
	// 	}
	// 	gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
	// 	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(fbWidth), int32(fbHeight), 0, gl.RGBA, gl.FLOAT, nil)
	// 	r.offscreenRenderer.width = fbWidth
	// 	r.offscreenRenderer.height = fbHeight
	// }

	// uniforms := &inputs.Uniforms{
	// 	Time:  float32(time),
	// 	Mouse: mouseData,
	// 	Frame: frameCount,
	// }

	var renderWidth, renderHeight int

	if r.recordMode {
		// In record mode, the render size is fixed to the stored dimensions.
		renderWidth = r.width
		renderHeight = r.height
	} else {
		// In interactive mode, match the window's framebuffer size to allow resizing.
		fbWidth, fbHeight := r.context.GetFramebufferSize()
		renderWidth = fbWidth
		renderHeight = fbHeight

		// Resize all resources if the window size changed.
		if fbWidth != r.offscreenRenderer.width || fbHeight != r.offscreenRenderer.height {
			r.offscreenRenderer.width = fbWidth
			r.offscreenRenderer.height = fbHeight
			gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
			gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(fbWidth), int32(fbHeight), 0, gl.RGBA, gl.FLOAT, nil)

			for _, buffer := range r.buffers {
				buffer.Resize(fbWidth, fbHeight)
			}
		}
	}

	uniforms := &inputs.Uniforms{
		Time:  float32(time),
		Mouse: mouseData,
		Frame: frameCount,
	}

	for _, pass := range r.bufferPasses {
		if pass.buffer != nil {
			pass.buffer.BindForWriting()
		}

		gl.UseProgram(pass.shaderProgram)
		updateUniforms(pass, renderWidth, renderHeight, uniforms)
		bindChannels(pass, uniforms)
		gl.Viewport(0, 0, int32(renderWidth), int32(renderHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT)
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
	gl.Clear(gl.COLOR_BUFFER_BIT)
	gl.BindVertexArray(r.quadVAO)
	gl.DrawArrays(gl.TRIANGLES, 0, 6)
	unbindChannels(imagePass)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
}

func (r *Renderer) Run() {
	startTime := r.context.Time()
	var frameCount int32 = 0
	var lastMouseClickX, lastMouseClickY float64
	var mouseWasDown bool

	for !r.context.ShouldClose() {
		currentTime := r.context.Time() - startTime
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

		r.RenderFrame(currentTime, frameCount, mouseData)

		fbWidth, fbHeight := r.context.GetFramebufferSize()
		gl.Viewport(0, 0, int32(fbWidth), int32(fbHeight))
		gl.Clear(gl.COLOR_BUFFER_BIT)
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
	if pass.frameLoc != -1 {
		gl.Uniform1i(pass.frameLoc, uniforms.Frame)
	}
	if pass.mouseLoc != -1 {
		gl.Uniform4f(pass.mouseLoc, uniforms.Mouse[0], uniforms.Mouse[1], uniforms.Mouse[2], uniforms.Mouse[3])
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

		if pass.iChannelResolutionLoc[chIndex] != -1 {
			res := ch.ChannelRes()
			gl.Uniform3fv(pass.iChannelResolutionLoc[chIndex], 1, &res[0])
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
