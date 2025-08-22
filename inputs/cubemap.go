package inputs

import (
	"fmt"
	"image"
	"image/draw"
	"log"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/api"
)

// CubeMapChannel represents a cube map texture input.
type CubeMapChannel struct {
	ctype      string
	textureID  uint32
	resolution [3]float32
	sampler    api.Sampler
}

// NewCubeMapChannel creates and initializes a new OpenGL cube map texture from six images.
func NewCubeMapChannel(images [6]image.Image, sampler api.Sampler) (*CubeMapChannel, error) {
	for i, img := range images {
		if img == nil {
			return nil, fmt.Errorf("input image for cube map face %d is nil", i)
		}
	}

	var textureID uint32
	gl.GenTextures(1, &textureID)
	gl.BindTexture(gl.TEXTURE_CUBE_MAP, textureID)

	var internalFormat int32 = gl.RGBA8
	if sampler.SRGB == "true" {
		internalFormat = gl.SRGB8_ALPHA8
		log.Printf("CubeMap Channel: Using sRGB texture format (srgb=true)")
	}

	// Load all 6 images in their standard order without any flipping.
	// The `texture` function in GLSL for samplerCube is designed to handle
	// the coordinate orientation correctly, assuming the image data is not pre-flipped.
	for i := 0; i < 6; i++ {
		img := images[i]

		// Convert the input image to RGBA, which is what OpenGL expects.
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, image.Point{}, draw.Src)

		width := int32(rgba.Bounds().Dx())
		height := int32(rgba.Bounds().Dy())

		// Upload the raw, unflipped pixel data.
		gl.TexImage2D(
			gl.TEXTURE_CUBE_MAP_POSITIVE_X+uint32(i),
			0,
			internalFormat,
			width,
			height,
			0,
			gl.RGBA,
			gl.UNSIGNED_BYTE,
			gl.Ptr(rgba.Pix),
		)
	}

	gl.TexParameteri(gl.TEXTURE_CUBE_MAP, gl.TEXTURE_WRAP_S, getWrapMode(sampler.Wrap))
	gl.TexParameteri(gl.TEXTURE_CUBE_MAP, gl.TEXTURE_WRAP_T, getWrapMode(sampler.Wrap))
	gl.TexParameteri(gl.TEXTURE_CUBE_MAP, gl.TEXTURE_WRAP_R, getWrapMode(sampler.Wrap))

	minFilter, magFilter := getFilterMode(sampler.Filter)
	gl.TexParameteri(gl.TEXTURE_CUBE_MAP, gl.TEXTURE_MIN_FILTER, minFilter)
	gl.TexParameteri(gl.TEXTURE_CUBE_MAP, gl.TEXTURE_MAG_FILTER, magFilter)

	if sampler.Filter == "mipmap" {
		gl.GenerateMipmap(gl.TEXTURE_CUBE_MAP)
	}

	gl.BindTexture(gl.TEXTURE_CUBE_MAP, 0) // Unbind texture

	width := images[0].Bounds().Dx()
	height := images[0].Bounds().Dy()

	return &CubeMapChannel{
		ctype:     "cubemap",
		textureID: textureID,
		resolution: [3]float32{
			float32(width),
			float32(height),
			1.0,
		},
		sampler: sampler,
	}, nil
}

// IChannel Interface Implementation

func (c *CubeMapChannel) GetCType() string          { return c.ctype }
func (c *CubeMapChannel) Update(uniforms *Uniforms) { /* No-op for static cube maps. */ }
func (c *CubeMapChannel) GetTextureID() uint32      { return c.textureID }
func (c *CubeMapChannel) ChannelRes() [3]float32    { return c.resolution }
func (c *CubeMapChannel) Destroy()                  { gl.DeleteTextures(1, &c.textureID) }
func (c *CubeMapChannel) GetSamplerType() string    { return "samplerCube" }
