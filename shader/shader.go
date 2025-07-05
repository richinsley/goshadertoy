package shader

import (
	"fmt"

	inputs "github.com/richinsley/goshadertoy/inputs"
)

// A simple vertex shader for drawing a fullscreen quad.
const vertexShaderSource = `#version 410 core
layout (location = 0) in vec2 in_vert;
out vec2 frag_uv;
void main() {
	frag_uv = in_vert * 0.5 + 0.5;
    gl_Position = vec4(in_vert, 0.0, 1.0);
}
`

// The blit fragment shader is used to copy a texture to the screen.
const blitFragmentShaderSourceFlip = `#version 410 core
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;

void main() {
    // Flip the y-coordinate by subtracting it from 1.0
    fragColor = texture(u_texture, vec2(frag_uv.x, 1.0 - frag_uv.y));
}
`

const blitFragmentShaderSource = `#version 410 core
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;

void main() {
    fragColor = texture(u_texture, frag_uv);
}
`

func GenerateVertexShader() string {
	return vertexShaderSource
}

func GetBlitFragmentShader(flip bool) string {
	if flip {
		return blitFragmentShaderSourceFlip
	}
	return blitFragmentShaderSource
}

// GeneratePreamble creates the GLSL preamble with dynamic sampler types.
func GeneratePreamble(channels []inputs.IChannel) string {
	basePreamble := `#version 300 es
precision highp float;
precision highp int;
precision mediump sampler3D;

// it's 2025, so this is always 1
#define HW_PERFORMANCE 1

uniform vec3      iResolution;           // viewport resolution (in pixels)
uniform float     iTime;                 // shader playback time (in seconds)
uniform float     iTimeDelta;            // render time (in seconds)
uniform float     iFrameRate;            // shader frame rate
uniform int       iFrame;                // shader playback frame
uniform float     iChannelTime[4];       // channel playback time (in seconds)
uniform vec3      iChannelResolution[4]; // channel resolution (in pixels)
uniform vec4      iMouse;                // mouse pixel coords. xy: current (if MLB down), zw: click
uniform vec4      iDate;                 // (year, month, day, time in seconds)
uniform float     iSampleRate;           // sound sample rate (i.e., 44100)
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

func GetMain() string {
	return `
void main( void )
{
    // The mainImage function expects pixel coordinates, which gl_FragCoord provides.
    mainImage( fragColor, gl_FragCoord.xy );
}
`
}

// GetFragmentShader combines the dynamic preamble, the user's shader code, and the main wrapper.
func GetFragmentShader(channels []inputs.IChannel, commoncode, shadercode string) string {
	return GeneratePreamble(channels) + commoncode + shadercode + GetMain()
}
