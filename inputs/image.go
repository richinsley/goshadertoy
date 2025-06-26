// inputs/image.go
package inputs

import (
	"fmt"
	"image"
	"image/draw"
	"log"

	"github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
)

// ImageChannel represents a static image texture input.
type ImageChannel struct {
	index      int
	ctype      string
	textureID  uint32
	resolution [3]float32
	sampler    api.Sampler
}

// vflip vertically flips the provided RGBA image. This is necessary when
// Shadertoy's vflip flag is true, to match the expected texture orientation.
func vflip(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	flipped := image.NewRGBA(bounds)
	height := bounds.Dy()

	// This is faster than calling At/Set for each pixel
	rowSize := bounds.Dx() * 4 // 4 bytes per pixel (RGBA)
	for y := 0; y < height; y++ {
		srcRow := src.Pix[((height-1)-y)*src.Stride:]
		dstRow := flipped.Pix[y*flipped.Stride:]
		copy(dstRow, srcRow[:rowSize])
	}
	return flipped
}

// NewImageChannel creates and initializes a new OpenGL texture from an image.
func NewImageChannel(index int, img image.Image, sampler api.Sampler) (*ImageChannel, error) {
	if img == nil {
		return nil, fmt.Errorf("input image for channel %d is nil", index)
	}

	// Convert source image to RGBA for consistency.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, image.Point{}, draw.Src)

	// Handle vertical flip if requested.
	if sampler.VFlip == "true" {
		log.Printf("Channel %d: Applying vertical flip (vflip=true)", index)
		rgba = vflip(rgba)
	}

	width := int32(rgba.Rect.Size().X)
	height := int32(rgba.Rect.Size().Y)

	var textureID uint32
	gl.GenTextures(1, &textureID)
	gl.BindTexture(gl.TEXTURE_2D, textureID)

	// Determine the correct internal format for the texture.
	// This is critical for sRGB correctness and for float textures.
	var internalFormat int32 = gl.RGBA8 // Default to 8-bit per channel RGBA.
	if sampler.Internal == "float" {
		// Use a 16-bit floating point format for higher precision.
		internalFormat = gl.RGBA16F
		log.Printf("Channel %d: Using float texture format (internal=float)", index)
	} else if sampler.SRGB == "true" {
		// Use an sRGB format. The GPU will automatically linearize colors when sampled.
		internalFormat = gl.SRGB8_ALPHA8
		log.Printf("Channel %d: Using sRGB texture format (srgb=true)", index)
	}

	// Set texture parameters (wrapping and filtering).
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, getWrapMode(sampler.Wrap))
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, getWrapMode(sampler.Wrap))

	minFilter, magFilter := getFilterMode(sampler.Filter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, minFilter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, magFilter)

	// Upload the image data to the GPU using the determined internal format.
	// The source data is still provided as RGBA with unsigned bytes; OpenGL handles the conversion.
	gl.TexImage2D(
		gl.TEXTURE_2D,
		0,
		internalFormat, // Use the correct format determined above.
		width,
		height,
		0,
		gl.RGBA,
		gl.UNSIGNED_BYTE,
		gl.Ptr(rgba.Pix),
	)

	// Generate mipmaps if the filter requires it.
	if sampler.Filter == "mipmap" {
		gl.GenerateMipmap(gl.TEXTURE_2D)
	}

	gl.BindTexture(gl.TEXTURE_2D, 0) // Unbind texture

	return &ImageChannel{
		index:     index,
		ctype:     "texture",
		textureID: textureID,
		resolution: [3]float32{
			float32(width),
			float32(height),
			1.0,
		},
		sampler: sampler,
	}, nil
}

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

// --- IChannel Interface Implementation ---
func (c *ImageChannel) GetInputIndex() int {
	return c.index
}

func (c *ImageChannel) GetCType() string {
	return c.ctype
}

func (c *ImageChannel) Update(uniforms *Uniforms) {
	// No-op for static images.
}

func (c *ImageChannel) GetTextureID() uint32 {
	return c.textureID
}

func (c *ImageChannel) ChannelRes() [3]float32 {
	return c.resolution
}

func (c *ImageChannel) Destroy() {
	gl.DeleteTextures(1, &c.textureID)
}

func (c *ImageChannel) GetSamplerType() string {
	// All image inputs are currently treated as 2D textures.
	return "sampler2D"
}
