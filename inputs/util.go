package inputs

import (
	"github.com/go-gl/gl/v4.1-core/gl"
)

// Helper to convert Shadertoy wrap string to OpenGL constant.
func getWrapMode(wrap string) int32 {
	switch wrap {
	case "repeat":
		return gl.REPEAT
	case "clamp":
		return gl.CLAMP_TO_EDGE
	default:
		return gl.REPEAT // Default behavior
	}
}

// Helper to convert Shadertoy filter string to OpenGL constants.
func getFilterMode(filter string) (minFilter, magFilter int32) {
	switch filter {
	case "mipmap":
		return gl.LINEAR_MIPMAP_LINEAR, gl.LINEAR
	case "linear":
		return gl.LINEAR, gl.LINEAR
	case "nearest":
		return gl.NEAREST, gl.NEAREST
	default:
		return gl.LINEAR, gl.LINEAR // Default behavior
	}
}
