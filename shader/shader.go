package shader

import (
	"fmt"

	inputs "github.com/richinsley/goshadertoy/inputs"
)

// ────────────────────────────────── Desktop GL ──────────────────────────────────

const vertexShaderSourceGL = `#version 410 core
layout (location = 0) in vec2 in_vert;
out vec2 frag_uv;
void main() {
    frag_uv = in_vert * 0.5 + 0.5;
    gl_Position = vec4(in_vert, 0.0, 1.0);
}
`

// YUV conversion with γ-correction + unbiased rounding
const yuvFragmentShaderSourceGL = `#version 410 core
in  vec2 frag_uv;
layout(location = 0) out uint y_out;
layout(location = 1) out uint u_out;
layout(location = 2) out uint v_out;

uniform sampler2D u_texture;   // linear RGB input
uniform int       u_bitDepth;  // 8 or 10

// BT.709 (R'G'B' -> Y'Cb'Cr')
// This matrix is constructed with column vectors to match GLSL's column-major memory layout.
const mat3 RGB_TO_YUV = mat3(
    vec3( 0.2126, -0.1146,  0.5000), // Column 0
    vec3( 0.7152, -0.3854, -0.4542), // Column 1
    vec3( 0.0722,  0.5000, -0.0458)  // Column 2
);

// Linear -> sRGB (BT.709) transfer
vec3 linearToSRGB(vec3 l)
{
    bvec3 cutoff = lessThanEqual(l, vec3(0.0031308));
    vec3  low    = l * 12.92;
    vec3  high   = 1.055 * pow(l, vec3(1.0 / 2.4)) - 0.055;
    return mix(high, low, cutoff);
}

void main()
{
    // flip the v coordinate
    vec2 nfrag_uv = vec2(frag_uv.x, 1.0 - frag_uv.y);
    vec3 rgb_in = texture(u_texture, nfrag_uv).rgb;
    vec3 rgb_p; // This will hold the sRGB / gamma-corrected value

    if (u_bitDepth > 8) {
        // For high bit depth, the input texture is linear (e.g., RGBA16F),
        // so we must convert it to sRGB before the YUV matrix.
        rgb_p = linearToSRGB(rgb_in);
    } else {
        // For 8-bit, the input texture is already sRGB (RGBA8),
        // so we use its value directly.
        rgb_p = rgb_in;
    }

    // 2) R'G'B' -> Y'Cb'Cr' (Y in [0..1], C in [-0.5..+0.5])
    vec3 yuv = RGB_TO_YUV * rgb_p;

    // 3) quantise to TV-range with unbiased rounding
    if (u_bitDepth > 8) {
        y_out = uint(round(clamp(yuv.x * 876.0 +  64.0,  64.0, 940.0))); // 10-bit
        u_out = uint(round(clamp(yuv.y * 896.0 + 512.0,  64.0, 960.0)));
        v_out = uint(round(clamp(yuv.z * 896.0 + 512.0,  64.0, 960.0)));
    } else {
        y_out = uint(round(clamp(yuv.x * 219.0 +  16.0,  16.0, 235.0))); // 8-bit
        u_out = uint(round(clamp(yuv.y * 224.0 + 128.0,  16.0, 240.0)));
        v_out = uint(round(clamp(yuv.z * 224.0 + 128.0,  16.0, 240.0)));
    }
}
`

const blitFragmentShaderSourceFlipGL = `#version 410 core
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;
void main() { fragColor = texture(u_texture, vec2(frag_uv.x, 1.0 - frag_uv.y)); }
`

const blitFragmentShaderSourceGL = `#version 410 core
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;
void main() { fragColor = texture(u_texture, frag_uv); }
`

// ──────────────────────────────────── GLES ──────────────────────────────────────

const vertexShaderSourceGLES = `#version 300 es
layout (location = 0) in vec2 in_vert;
out vec2 frag_uv;
void main() {
    frag_uv = in_vert * 0.5 + 0.5;
    gl_Position = vec4(in_vert, 0.0, 1.0);
}
`

// GLES version
const yuvFragmentShaderSourceGLES = `#version 300 es
precision highp float;
precision highp int;

in  vec2 frag_uv;
layout(location = 0) out uint y_out;
layout(location = 1) out uint u_out;
layout(location = 2) out uint v_out;

uniform sampler2D u_texture;
uniform int       u_bitDepth;

// BT.709 (R'G'B' -> Y'Cb'Cr')
// This matrix is constructed with column vectors to match GLSL's column-major memory layout.
const mat3 RGB_TO_YUV = mat3(
    vec3( 0.2126, -0.1146,  0.5000), // Column 0
    vec3( 0.7152, -0.3854, -0.4542), // Column 1
    vec3( 0.0722,  0.5000, -0.0458)  // Column 2
);

// Linear -> sRGB transfer
vec3 linearToSRGB(vec3 l) {
    vec3 low  = 12.92 * l;
    vec3 high = 1.055 * pow(l, vec3(1.0 / 2.4)) - 0.055;
    return mix(high, low, step(l, vec3(0.0031308)));
}

void main()
{
    // flip the v coordinate
    vec2 nfrag_uv = vec2(frag_uv.x, 1.0 - frag_uv.y);
    vec3 rgb_in = texture(u_texture, nfrag_uv).rgb;
    vec3 rgb_p; // This will hold the sRGB / gamma-corrected value

    if (u_bitDepth > 8) {
        // For high bit depth, the input texture is linear (e.g., RGBA16F),
        // so we must convert it to sRGB before the YUV matrix.
        rgb_p = linearToSRGB(rgb_in);
    } else {
        // For 8-bit, the input texture is already sRGB (RGBA8),
        // so we use its value directly.
        rgb_p = rgb_in;
    }

    // 2) R'G'B' -> Y'Cb'Cr' (Y in [0..1], C in [-0.5..+0.5])
    vec3 yuv = RGB_TO_YUV * rgb_p;

    // 3) quantise to TV-range with unbiased rounding
    if (u_bitDepth > 8) {
        y_out = uint(round(clamp(yuv.x * 876.0 +  64.0,  64.0, 940.0))); // 10-bit
        u_out = uint(round(clamp(yuv.y * 896.0 + 512.0,  64.0, 960.0)));
        v_out = uint(round(clamp(yuv.z * 896.0 + 512.0,  64.0, 960.0)));
    } else {
        y_out = uint(round(clamp(yuv.x * 219.0 +  16.0,  16.0, 235.0))); // 8-bit
        u_out = uint(round(clamp(yuv.y * 224.0 + 128.0,  16.0, 240.0)));
        v_out = uint(round(clamp(yuv.z * 224.0 + 128.0,  16.0, 240.0)));
    }
}
`

const blitFragmentShaderSourceFlipGLES = `#version 300 es
precision mediump float;
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;
void main() { fragColor = texture(u_texture, vec2(frag_uv.x, 1.0 - frag_uv.y)); }
`

const blitFragmentShaderSourceGLES = `#version 300 es
precision mediump float;
in vec2 frag_uv;
out vec4 fragColor;
uniform sampler2D u_texture;
void main() { fragColor = texture(u_texture, frag_uv); }
`

// GenerateSoundShaderSource creates the full WebGL source for a sound shader.
func GenerateSoundShaderSource(commonCode, soundShader string, channels []inputs.IChannel) string {
	// The preamble includes all standard uniforms a sound shader might need.
	preamble := `#version 300 es
precision highp float;
precision highp int;
precision mediump sampler3D;

#define HW_PERFORMANCE 1

uniform float iTimeOffset;
uniform int   iSampleOffset;
uniform vec4  iDate;
uniform float iSampleRate;
uniform vec3  iChannelResolution[4];
uniform float iChannelTime[4];
`
	// Declare iChannelN samplers based on the provided channel types.
	for i := 0; i < 4; i++ {
		sampler := "sampler2D"
		if channels != nil && i < len(channels) && channels[i] != nil {
			sampler = channels[i].GetSamplerType()
		}
		preamble += fmt.Sprintf("uniform %s iChannel%d;\n", sampler, i)
	}

	// The main function that Shadertoy uses for sound shaders.
	// It calls the user-provided mainSound function.
	mainWrapper := `
out vec4 outColor;
void main()
{
    float t = iTimeOffset + ((gl_FragCoord.x-0.5) + (gl_FragCoord.y-0.5)*512.0)/iSampleRate;
    int   s = iSampleOffset + int(gl_FragCoord.y-0.5)*512 + int(gl_FragCoord.x-0.5);

    // Call the user's mainSound function, which might be mainSound(t) or mainSound(s, t)
    // We will assume the more complex one is available if defined.
    vec2 y = mainSound( s, t );

    vec2 v  = floor((0.5+0.5*y)*65536.0);
    vec2 vl =   mod(v,256.0)/255.0;
    vec2 vh = floor(v/256.0)/255.0;
    outColor = vec4(vl.x,vh.x,vl.y,vh.y);
}
`
	// Combine all parts. The user's soundShader string is expected to contain the mainSound function.
	// We also need to add a dummy mainSound(s,t) if only mainSound(t) is provided.
	soundShaderCode := soundShader
	// if !strings.Contains(soundShader, "mainSound( int, float )") {
	// 	soundShaderCode += "\nvec2 mainSound( int s, float t ) { return mainSound(t); }\n"
	// }

	return preamble + commonCode + "\n" + soundShaderCode + "\n" + mainWrapper
}

// ────────────────────────────────── Public API ─────────────────────────────────

func GenerateVertexShader(isGLES bool) string {
	if isGLES {
		return vertexShaderSourceGLES
	}
	return vertexShaderSourceGL
}

func GetYUVFragmentShader(isGLES bool) string {
	if isGLES {
		return yuvFragmentShaderSourceGLES
	}
	return yuvFragmentShaderSourceGL
}

func GetBlitFragmentShader(flip, isGLES bool) string {
	if isGLES {
		if flip {
			return blitFragmentShaderSourceFlipGLES
		}
		return blitFragmentShaderSourceGLES
	}
	if flip {
		return blitFragmentShaderSourceFlipGL
	}
	return blitFragmentShaderSourceGL
}

// ────────────────────── Dynamic preamble / user code glue ──────────────────────

func GeneratePreamble(channels []inputs.IChannel) string {
	base := `#version 300 es
precision highp float;
precision highp int;
precision mediump sampler3D;

#define HW_PERFORMANCE 1

uniform vec3  iResolution;
uniform float iTime;
uniform float iTimeDelta;
uniform float iFrameRate;
uniform int   iFrame;
uniform float iChannelTime[4];
uniform vec3  iChannelResolution[4];
uniform vec4  iMouse;
uniform vec4  iDate;
uniform float iSampleRate;
`
	// declare iChannelN samplers
	for i := 0; i < 4; i++ {
		sampler := "sampler2D"
		if channels[i] != nil {
			sampler = channels[i].GetSamplerType()
		}
		base += fmt.Sprintf("uniform %s iChannel%d;\n", sampler, i)
	}

	// helper funcs
	return base + `
in vec2 frag_coord_uv;
out vec4 fragColor;

#define FAST_TANH_BODY(x) ((x) * (27.0 + (x)*(x)) / (27.0 + 9.0*(x)*(x)))
float fast_tanh(float x) { return FAST_TANH_BODY(x); }
vec2  fast_tanh(vec2  x) { return FAST_TANH_BODY(x); }
vec3  fast_tanh(vec3  x) { return FAST_TANH_BODY(x); }
vec4  fast_tanh(vec4  x) { return FAST_TANH_BODY(x); }
#define tanh fast_tanh
`
}

func GetMain() string {
	return `
void main(void)
{
    mainImage(fragColor, gl_FragCoord.xy);
}
`
}

// Combine preamble + user common + user frag + wrapper
func GetFragmentShader(ch []inputs.IChannel, common, user string) string {
	return GeneratePreamble(ch) + common + user + GetMain()
}
