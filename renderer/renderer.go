package renderer

import (
	"fmt"
	"strings"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/glfwcontext"
	"github.com/richinsley/goshadertoy/inputs"
	gst "github.com/richinsley/goshadertranslator"
)

// Renderer encapsulates the OpenGL state for drawing a shader.
type Renderer struct {
	// The context is now provided by the dedicated glfwcontext package.
	context       *glfwcontext.Context
	shaderProgram uint32
	quadVAO       uint32
	channels      []inputs.IChannel

	// Uniform locations are cached for performance.
	resolutionLoc         int32
	timeLoc               int32
	mouseLoc              int32
	iChannelLoc           [4]int32
	iChannelResolutionLoc [4]int32
}

// NewRenderer creates a new renderer and initializes its graphics context.
func NewRenderer() (*Renderer, error) {
	r := &Renderer{}
	var err error

	// We now instantiate the context from the new package.
	r.context, err = glfwcontext.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize glfw context: %w", err)
	}
	return r, nil
}

// Shutdown cleans up OpenGL objects and terminates the context.
func (r *Renderer) Shutdown() {
	gl.DeleteProgram(r.shaderProgram)
	gl.DeleteVertexArrays(1, &r.quadVAO)

	// Clean up channel resources
	for _, ch := range r.channels {
		if ch != nil {
			ch.Destroy()
		}
	}

	r.context.Shutdown()
}

// Fullscreen quad vertices.
var quadVertices = []float32{
	-1.0, 1.0, -1.0, -1.0, 1.0, -1.0,
	-1.0, 1.0, 1.0, -1.0, 1.0, 1.0,
}

// InitScene compiles shaders and sets up vertex data.
func (r *Renderer) InitScene(vertexShaderSource, fragmentShaderSource string, uniformMap map[string]gst.ShaderVariable, channels []inputs.IChannel) error {
	var err error
	r.shaderProgram, err = newProgram(vertexShaderSource, fragmentShaderSource)
	if err != nil {
		return fmt.Errorf("failed to create shader program: %w", err)
	}
	r.channels = channels // Store channels

	gl.UseProgram(r.shaderProgram)

	// Query uniform locations using the mapped names from the translator.
	r.resolutionLoc = -1
	r.timeLoc = -1
	r.mouseLoc = -1
	if v, ok := uniformMap["iResolution"]; ok {
		r.resolutionLoc = gl.GetUniformLocation(r.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}
	if v, ok := uniformMap["iTime"]; ok {
		r.timeLoc = gl.GetUniformLocation(r.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}
	if v, ok := uniformMap["iMouse"]; ok {
		r.mouseLoc = gl.GetUniformLocation(r.shaderProgram, gl.Str(v.MappedName+"\x00"))
	}

	// iChannel0 to iChannel3
	for i := 0; i < 4; i++ {
		samplerName := fmt.Sprintf("iChannel%d", i)
		resolutionName := fmt.Sprintf("iChannelResolution[%d]", i)
		r.iChannelLoc[i] = -1
		r.iChannelResolutionLoc[i] = -1
		if v, ok := uniformMap[samplerName]; ok {
			r.iChannelLoc[i] = gl.GetUniformLocation(r.shaderProgram, gl.Str(v.MappedName+"\x00"))
		}
		if v, ok := uniformMap[resolutionName]; ok {
			r.iChannelResolutionLoc[i] = gl.GetUniformLocation(r.shaderProgram, gl.Str(v.MappedName+"\x00"))
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

	return nil
}

// Run starts the main render loop. It now handles all standard uniform updates internally.
func (r *Renderer) Run() {
	startTime := r.context.Time()
	win := r.context.Window()

	var lastMouseClickX, lastMouseClickY float64
	var mouseWasDown bool

	for !r.context.ShouldClose() {
		currentTime := r.context.Time() - startTime
		width, height := r.context.GetFramebufferSize()

		gl.UseProgram(r.shaderProgram)

		// Update standard uniforms
		if r.resolutionLoc != -1 {
			gl.Uniform3f(r.resolutionLoc, float32(width), float32(height), 0)
		}
		if r.timeLoc != -1 {
			gl.Uniform1f(r.timeLoc, float32(currentTime))
		}

		// Update iMouse uniform and prepare data for channels
		var mouseData [4]float32
		if r.mouseLoc != -1 && win != nil {
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
			gl.Uniform4f(r.mouseLoc, mouseData[0], mouseData[1], mouseData[2], mouseData[3])
		}

		// Update and bind input channels
		uniforms := &inputs.Uniforms{
			Time:  float32(currentTime),
			Mouse: mouseData,
		}

		for _, ch := range r.channels {
			if ch == nil {
				continue
			}

			ch.Update(uniforms) // Update channel state (for dynamic inputs)

			chIndex := ch.GetInputIndex()

			var texTarget uint32
			switch ch.GetSamplerType() {
			case "sampler3D":
				texTarget = gl.TEXTURE_3D
			case "samplerCube":
				texTarget = gl.TEXTURE_CUBE_MAP
			default:
				texTarget = gl.TEXTURE_2D
			}

			if r.iChannelLoc[chIndex] != -1 {
				gl.ActiveTexture(gl.TEXTURE0 + uint32(chIndex))
				gl.BindTexture(texTarget, ch.GetTextureID())
				gl.Uniform1i(r.iChannelLoc[chIndex], int32(chIndex))
			}

			// Set resolution uniform
			if r.iChannelResolutionLoc[chIndex] != -1 {
				res := ch.ChannelRes()
				gl.Uniform3fv(r.iChannelResolutionLoc[chIndex], 1, &res[0])
			}
		}

		// Render the scene
		gl.Clear(gl.COLOR_BUFFER_BIT)
		gl.ClearColor(0.0, 0.0, 0.0, 1.0)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)
		gl.BindVertexArray(0)

		// Unbind textures
		for _, ch := range r.channels {
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
				gl.ActiveTexture(gl.TEXTURE0 + uint32(ch.GetInputIndex()))
				gl.BindTexture(texTarget, 0)
			}
		}

		r.context.EndFrame()
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
