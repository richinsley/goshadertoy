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
	shaderProgram uint32
	channels      []inputs.IChannel
	buffer        *inputs.Buffer // The buffer this pass renders to (if any)
	// Uniform locations are cached for performance.
	resolutionLoc         int32
	timeLoc               int32
	mouseLoc              int32
	frameLoc              int32
	iChannelLoc           [4]int32
	iChannelResolutionLoc [4]int32
}

// Renderer encapsulates the OpenGL state for drawing a shader.
type Renderer struct {
	// The context is provided by the dedicated glfwcontext package.
	context      *glfwcontext.Context
	quadVAO      uint32
	bufferPasses []*RenderPass          // ordered list of buffer render passes
	namedPasses  map[string]*RenderPass // named render passes for easy access
	buffers      map[string]*inputs.Buffer
}

// NewRenderer creates a new renderer and initializes its graphics context.
func NewRenderer() (*Renderer, error) {
	r := &Renderer{}
	var err error

	r.namedPasses = make(map[string]*RenderPass)
	r.bufferPasses = make([]*RenderPass, 0)
	r.buffers = make(map[string]*inputs.Buffer)

	// We now instantiate the context from the new package.
	r.context, err = glfwcontext.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw context: %w", err)
	}
	return r, nil
}

// Shutdown cleans up OpenGL objects and terminates the context.
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
				// avoid double destroying buffers
				if _, ok := ch.(*inputs.Buffer); !ok {
					ch.Destroy()
				}
			}
		}
	}

	gl.DeleteVertexArrays(1, &r.quadVAO)
	r.context.Shutdown()
}

// Fullscreen quad vertices.
var quadVertices = []float32{
	-1.0, 1.0, -1.0, -1.0, 1.0, -1.0,
	-1.0, 1.0, 1.0, -1.0, 1.0, 1.0,
}

func (r *Renderer) GetRenderPass(name string, shaderArgs *api.ShaderArgs) (*RenderPass, error) {

	// Create a new RenderPass if it doesn't exist
	if name == "" {
		name = "image" // Default to image pass
	}

	var passArgs *api.BufferRenderPass
	var exists bool
	if passArgs, exists = shaderArgs.Buffers[name]; !exists {
		return nil, fmt.Errorf("no render pass found with name: %s", name)
	}

	width, height := r.context.GetFramebufferSize()

	// Create IChannel objects from shader arguments
	channels, err := inputs.GetChannels(passArgs.Inputs, width, height, r.quadVAO, r.buffers)
	if err != nil {
		return nil, fmt.Errorf("failed to create channels: %w", err)
	}

	// Generate the full fragment shader source
	fullFragmentSource := shader.GetFragmentShader(channels, shaderArgs.CommonCode, passArgs.Code)

	// translate the shader to GLSL
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

	// get the standard vertex shader source
	vertexShaderSource := shader.GenerateVertexShader()

	retv.shaderProgram, err = newProgram(vertexShaderSource, fsShader.Code)
	if err != nil {
		return nil, fmt.Errorf("failed to create shader program: %w", err)
	}
	retv.channels = channels // Store channels
	uniformMap := fsShader.Variables
	gl.UseProgram(retv.shaderProgram)

	// Query uniform locations using the mapped names from the translator.
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

	// iChannel0 to iChannel3
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

// InitScene compiles shaders and sets up vertex data.
func (r *Renderer) InitScene(shaderArgs *api.ShaderArgs) error {
	// see if we need a translator
	var err error
	if translator == nil {
		ctx := context.Background()
		translator, err = gst.NewShaderTranslator(ctx)
		if err != nil {
			return err
		}
	}
	// Create Vertex Array Object (VAO) and Vertex Buffer Object (VBO) for the quad.
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

	// Create the buffer objects first
	width, height := r.context.GetFramebufferSize()
	for _, name := range []string{"A", "B", "C", "D"} {
		if _, exists := shaderArgs.Buffers[name]; exists {
			buffer, err := inputs.NewBuffer(width, height, r.quadVAO)
			if err != nil {
				return fmt.Errorf("failed to create buffer %s: %w", name, err)
			}
			r.buffers[name] = buffer
		}
	}

	// Create the image pass and any buffer passes.
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

// Run starts the main render loop. It handles all standard uniform updates internally.
func (r *Renderer) Run() {
	startTime := r.context.Time()
	win := r.context.Window()
	var lastMouseClickX, lastMouseClickY float64
	var mouseWasDown bool
	var frameCount int32 = 0
	// Keep track of the last size to detect changes.
	var lastWidth, lastHeight int = -1, -1

	for !r.context.ShouldClose() {
		currentTime := r.context.Time() - startTime
		width, height := r.context.GetFramebufferSize()

		// If the window size has changed, resize the buffers.
		if width != lastWidth || height != lastHeight {
			for _, buffer := range r.buffers {
				buffer.Resize(width, height)
			}
			lastWidth, lastHeight = width, height
		}

		// Prepare uniform data that is common to all passes
		var mouseData [4]float32
		if win != nil {
			x, y := win.GetCursorPos()
			mouseX := float32(x)
			mouseY := float32(height) - float32(y) // Flip Y
			const mouseLeft = 0
			isMouseDown := win.GetMouseButton(mouseLeft) == 1 // 1 is glfw.Press
			if isMouseDown && !mouseWasDown {
				lastMouseClickX, lastMouseClickY = x, y
			}
			mouseWasDown = isMouseDown
			clickX := float32(lastMouseClickX)
			clickY := float32(height) - float32(lastMouseClickY) // Flip Y
			if !isMouseDown {
				// Shadertoy negates z/w when the mouse button is up
				clickX = -clickX
				clickY = -clickY
			}
			mouseData = [4]float32{mouseX, mouseY, clickX, clickY}
		}
		uniforms := &inputs.Uniforms{
			Time:  float32(currentTime),
			Mouse: mouseData,
			Frame: frameCount,
		}
		// Render buffer passes
		for _, pass := range r.bufferPasses {
			if pass.buffer != nil {
				pass.buffer.BindForWriting()
			}

			gl.UseProgram(pass.shaderProgram)
			updateUniforms(pass, width, height, uniforms)
			bindChannels(pass, uniforms)
			gl.Viewport(0, 0, int32(width), int32(height))
			gl.Clear(gl.COLOR_BUFFER_BIT)
			gl.BindVertexArray(r.quadVAO)
			gl.DrawArrays(gl.TRIANGLES, 0, 6)
			unbindChannels(pass)

			if pass.buffer != nil {
				pass.buffer.UnbindForWriting()
				pass.buffer.SwapBuffers()
			}
		}
		// Render the final image pass to the screen
		imagePass := r.namedPasses["image"]
		gl.UseProgram(imagePass.shaderProgram)
		updateUniforms(imagePass, width, height, uniforms)
		bindChannels(imagePass, uniforms)
		gl.Viewport(0, 0, int32(width), int32(height))
		gl.Clear(gl.COLOR_BUFFER_BIT)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)
		unbindChannels(imagePass)
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

// newProgram compiles and links the GLSL shaders.
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

// compileShader handles compilation of a single shader.
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
