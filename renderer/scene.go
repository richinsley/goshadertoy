// In renderer/scene.go
package renderer

import (
	"fmt"
	"log"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
	"github.com/richinsley/goshadertoy/inputs"
	"github.com/richinsley/goshadertoy/options"
	"github.com/richinsley/goshadertoy/shader"
	xlate "github.com/richinsley/goshadertoy/translator"
	gst "github.com/richinsley/goshadertranslator"
)

// Scene encapsulates all the resources and render passes for a single Shadertoy shader.
type Scene struct {
	Title string
	// The final render pass that draws to the screen or primary FBO.
	ImagePass *RenderPass
	// The ordered list of buffer passes (A, B, C, D) that must execute before the ImagePass.
	BufferPasses []*RenderPass
	// A map for easy lookup of any pass by its Shadertoy name.
	NamedPasses map[string]*RenderPass
	// The offscreen buffers (FBOs) used by the buffer passes.
	Buffers map[string]*inputs.Buffer
	// A flat list of all unique channel resources (textures, cubemaps, etc.) for easy cleanup.
	allChannels []inputs.IChannel
}

// Destroy releases all OpenGL resources used by the scene.
// This is crucial for preventing memory leaks when switching scenes.
func (s *Scene) Destroy() {
	if s == nil {
		return
	}
	log.Printf("Destroying scene: %s", s.Title)

	// Destroy all unique channel resources (textures, etc.)
	// This avoids double-destroying buffers which are also channels.
	for _, ch := range s.allChannels {
		// Buffers are destroyed separately because they own FBOs.
		if _, isBuffer := ch.(*inputs.Buffer); !isBuffer {
			ch.Destroy()
		}
	}

	// Destroy buffer FBOs and textures
	for _, buffer := range s.Buffers {
		buffer.Destroy()
	}

	// Finally, destroy all shader programs
	for _, pass := range s.NamedPasses {
		gl.DeleteProgram(pass.ShaderProgram)
	}
}

// LoadScene creates and initializes a new Scene from parsed shader arguments.
func (r *Renderer) LoadScene(shaderArgs *api.ShaderArgs, options *options.ShaderOptions) (*Scene, error) {
	scene := &Scene{
		Title:        shaderArgs.Title,
		NamedPasses:  make(map[string]*RenderPass),
		BufferPasses: make([]*RenderPass, 0),
		Buffers:      make(map[string]*inputs.Buffer),
		allChannels:  make([]inputs.IChannel, 0),
	}

	width, height := r.width, r.height
	if !r.recordMode && r.context != nil {
		width, height = r.context.GetFramebufferSize()
	}

	// 1. Create Buffers for the Scene
	for _, name := range []string{"A", "B", "C", "D"} {
		if _, exists := shaderArgs.Buffers[name]; exists {
			buffer, err := inputs.NewBuffer(width, height, r.quadVAO)
			if err != nil {
				scene.Destroy() // cleanup on failure
				return nil, fmt.Errorf("failed to create buffer %s: %w", name, err)
			}
			scene.Buffers[name] = buffer
		}
	}

	// 2. Create Render Passes for the Scene
	passnames := []string{"A", "B", "C", "D", "image"}
	uniqueChannels := make(map[inputs.IChannel]struct{})

	for _, name := range passnames {
		if _, exists := shaderArgs.Buffers[name]; !exists {
			continue
		}

		// CORRECTED: Pass scene.Buffers to the helper
		pass, err := r.createRenderPass(name, shaderArgs, options, scene.Buffers)
		if err != nil {
			scene.Destroy() // cleanup on failure
			return nil, fmt.Errorf("failed to create render pass %s: %v", name, err)
		}

		scene.NamedPasses[name] = pass
		if name == "image" {
			scene.ImagePass = pass
		} else {
			pass.Buffer = scene.Buffers[name]
			scene.BufferPasses = append(scene.BufferPasses, pass)
		}

		for _, ch := range pass.Channels {
			if ch != nil {
				uniqueChannels[ch] = struct{}{}
			}
		}
	}

	for ch := range uniqueChannels {
		scene.allChannels = append(scene.allChannels, ch)
	}

	log.Printf("Successfully loaded scene: %s", scene.Title)
	return scene, nil
}

// createRenderPass is a new helper method refactored from the old GetRenderPass logic.
func (r *Renderer) createRenderPass(name string, shaderArgs *api.ShaderArgs, options *options.ShaderOptions, buffers map[string]*inputs.Buffer) (*RenderPass, error) {
	passArgs, exists := shaderArgs.Buffers[name]
	if !exists {
		return nil, fmt.Errorf("no render pass found with name: %s", name)
	}

	width, height := r.width, r.height
	if r.context != nil {
		width, height = r.context.GetFramebufferSize()
	}

	// This now uses the passed-in buffers map from the scene being built.
	channels, err := inputs.GetChannels(passArgs.Inputs, width, height, r.quadVAO, buffers, options, r.audioDevice)
	if err != nil {
		return nil, fmt.Errorf("failed to create channels: %w", err)
	}

	fullFragmentSource := shader.GetFragmentShader(channels, shaderArgs.CommonCode, passArgs.Code)
	outputFormat := gst.OutputFormatGLSL410
	if r.isGLES() {
		outputFormat = gst.OutputFormatESSL
	}
	translator := xlate.GetTranslator()
	fsShader, err := translator.TranslateShader(fullFragmentSource, "fragment", gst.ShaderSpecWebGL2, outputFormat)
	if err != nil {
		return nil, fmt.Errorf("fragment shader translation failed: %w", err)
	}

	retv := &RenderPass{
		ShaderProgram: 0,
		Channels:      channels,
	}

	vertexShaderSource := shader.GenerateVertexShader(r.isGLES())
	retv.ShaderProgram, err = newProgram(vertexShaderSource, fsShader.Code)
	if err != nil {
		return nil, fmt.Errorf("failed to create shader program: %w", err)
	}

	// get the standard uniforms
	uniformMap := fsShader.Variables
	gl.UseProgram(retv.ShaderProgram)
	retv.resolutionLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iResolution")
	retv.timeLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iTime")
	retv.mouseLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iMouse")
	retv.frameLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iFrame")
	retv.iDateLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iDate")
	retv.iSampleRateLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iSampleRate")
	retv.iTimeDeltaLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iTimeDelta")
	retv.iFrameRateLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iFrameRate")

	retv.iChannelTimeLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iChannelTime[0]")
	if retv.iChannelTimeLoc < 0 {
		retv.iChannelTimeLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iChannelTime")
	}

	retv.iChannelResolutionLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iChannelResolution[0]")
	if retv.iChannelResolutionLoc < 0 {
		retv.iChannelResolutionLoc = r.GetUniformLocation(uniformMap, retv.ShaderProgram, "iChannelResolution")
	}

	// iChannel uniforms
	for i := 0; i < 4; i++ {
		samplerName := fmt.Sprintf("iChannel%d", i)
		retv.iChannelLoc[i] = -1
		if v, ok := uniformMap[samplerName]; ok {
			retv.iChannelLoc[i] = gl.GetUniformLocation(retv.ShaderProgram, gl.Str(v.MappedName+"\x00"))
		}
	}

	return retv, nil
}
