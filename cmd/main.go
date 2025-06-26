package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"

	// The main package now only needs to know about the renderer, not its dependencies.
	shadertoy "github.com/richinsley/goshadertoy"
	inputs "github.com/richinsley/goshadertoy/inputs"
	renderer "github.com/richinsley/goshadertoy/renderer"
	gst "github.com/richinsley/goshadertranslator"
)

// A simple vertex shader for drawing a fullscreen quad.
const vertexShaderSource = `#version 410 core
layout (location = 0) in vec2 in_vert;
void main() {
    gl_Position = vec4(in_vert, 0.0, 1.0);
}
`

// generatePreamble creates the GLSL preamble with dynamic sampler types.
func generatePreamble(channels []inputs.IChannel) string {
	basePreamble := `#version 300 es
precision highp float;
precision highp int;
precision mediump sampler3D;

uniform vec3 iResolution;
uniform float iTime;
uniform vec4 iMouse;
uniform vec3 iChannelResolution[4];
uniform int iFrame;
`
	// Dynamically declare iChannel samplers based on their type
	channelDecls := ""
	for i := 0; i < 4; i++ {
		samplerType := "sampler2D" // Default to sampler2D
		if channels[i] != nil {
			samplerType = channels[i].GetSamplerType()
		}
		channelDecls += fmt.Sprintf("uniform %s iChannel%d;\n", samplerType, i)
	}

	// Other defines and helper functions here
	postamble := `
in vec2 frag_coord_uv;
out vec4 fragColor;

#define FAST_TANH_BODY(x)  ( (x) * (27.0 + (x)*(x)) / (27.0 + 9.0*(x)*(x)) )
float fast_tanh(float x) { return FAST_TANH_BODY(x); }
vec2  fast_tanh(vec2  x) { return FAST_TANH_BODY(x); }
vec3  fast_tanh(vec3  x) { return FAST_TANH_BODY(x); }
vec4  fast_tanh(vec4  x) { return FAST_TANH_BODY(x); }
#define tanh fast_tanh
`
	return basePreamble + channelDecls + postamble
}

func getMain() string {
	return `
void main( void )
{
    // The mainImage function expects pixel coordinates, which gl_FragCoord provides.
    mainImage( fragColor, gl_FragCoord.xy );
}
`
}

// GetFragmentShader combines the dynamic preamble, the user's shader code, and the main wrapper.
func GetFragmentShader(preamble, commoncode, shadercode string) string {
	return preamble + commoncode + shadercode + getMain()
}

func runShadertoy(shaderArgs *shadertoy.ShaderArgs) {
	// Initialize renderer
	r, err := renderer.NewRenderer()
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	// Create IChannel objects from shader arguments
	channels := make([]inputs.IChannel, 4)
	for i, chInput := range shaderArgs.Inputs {
		if chInput == nil {
			continue
		}

		channelIndex := chInput.Channel
		// Ensure channel index is valid
		if channelIndex < 0 || channelIndex >= 4 {
			log.Printf("Warning: Invalid channel index %d found, skipping.", channelIndex)
			continue
		}

		switch chInput.CType {
		case "texture":
			if chInput.Data == nil {
				log.Printf("Warning: Channel %d is a texture but has no image data, skipping.", i)
				continue
			}
			imgChannel, err := inputs.NewImageChannel(chInput.Channel, chInput.Data, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create image channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = imgChannel
			log.Printf("Initialized ImageChannel %d.", chInput.Channel)
		case "volume":
			if chInput.Volume == nil {
				log.Printf("Warning: Channel %d is a volume but has no data, skipping.", channelIndex)
				continue
			}
			volChannel, err := inputs.NewVolumeChannel(channelIndex, chInput.Volume, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create volume channel %d: %v", channelIndex, err)
			}
			channels[channelIndex] = volChannel
			log.Printf("Initialized VolumeChannel %d.", channelIndex)
		case "cubemap":
			isComplete := true
			for _, img := range chInput.CubeData {
				if img == nil {
					isComplete = false
					break
				}
			}
			if !isComplete {
				log.Printf("Warning: Channel %d is a cubemap but is missing image data, skipping.", i)
				continue
			}
			cubeChannel, err := inputs.NewCubeMapChannel(chInput.Channel, chInput.CubeData, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create cube map channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = cubeChannel
			log.Printf("Initialized CubeMapChannel %d.", chInput.Channel)
		case "buffer":
			log.Printf("Warning: Buffer inputs are not yet supported (Channel %d).", i)
		case "mic":
			newChannel, err := inputs.NewMicChannel(chInput.Channel)
			if err != nil {
				log.Fatalf("Failed to create image channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = newChannel
			log.Printf("Initialized MicChannel %d.", chInput.Channel)
		default:
			if chInput.CType != "" {
				log.Printf("Warning: Unsupported channel type '%s' for channel %d.", chInput.CType, i)
			}
		}
	}

	// Translate the shader
	log.Println("--- Translating Fragment Shader ---")
	ctx := context.Background()
	translator, err := gst.NewShaderTranslator(ctx)
	if err != nil {
		log.Fatalf("Failed to create shader translator: %v", err)
	}
	defer translator.Close()

	// fullFragmentSource := GetFragmentShader(shaderArgs.ShaderCode)
	preamble := generatePreamble(channels)
	fullFragmentSource := GetFragmentShader(preamble, shaderArgs.CommonCode, shaderArgs.ShaderCode)

	fsShader, err := translator.TranslateShader(fullFragmentSource, "fragment", gst.ShaderSpecWebGL2, gst.OutputFormatGLSL410)
	if err != nil {
		log.Fatalf("Fragment shader translation failed: %v", err)
	}

	log.Println("Fragment shader translated successfully.")

	// Initialize the scene with shaders and channels
	err = r.InitScene(vertexShaderSource, fsShader.Code, fsShader.Variables, channels)
	if err != nil {
		log.Fatalf("Failed to initialize scene: %v", err)
	}

	// Start the render loop
	log.Println("Starting render loop...")
	r.Run()
}

func init() {
	runtime.LockOSThread()
}
func main() {
	// do this in init() for now
	// runtime.LockOSThread()

	var apikey = flag.String("apikey", "", "Shadertoy API key (from SHADERTOY_KEY env var if not set)")
	var shaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID (e.g., 'Another Cloudy Tunnel 2')") // Default to one with an image
	var help = flag.Bool("help", false, "Show help message")
	flag.Parse()

	if *help {
		fmt.Println("Shadertoy Shader Viewer (GLFW+go-gl version)")
		flag.PrintDefaults()
		return
	}

	finalAPIKey := *apikey
	if finalAPIKey == "" {
		finalAPIKey = os.Getenv("SHADERTOY_KEY")
	}

	log.Printf("Fetching shader with ID: %s", *shaderID)
	shaderJSON, err := shadertoy.ShaderFromID(finalAPIKey, *shaderID, true)
	if err != nil {
		log.Fatalf("Error fetching shader from ID: %v", err)
	}

	shaderArgs, err := shadertoy.ShaderArgsFromJSON(shaderJSON, true)
	if err != nil {
		log.Fatalf("Error processing shader JSON: %v", err)
	}
	log.Printf("Successfully processed shader: %s", shaderArgs.Title)

	if !shaderArgs.Complete {
		log.Println("Warning: Shader arguments may be incomplete (e.g., missing textures or unsupported inputs).")
	}

	runShadertoy(shaderArgs)
}
