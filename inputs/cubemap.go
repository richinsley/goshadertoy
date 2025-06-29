package inputs

import (
	"fmt"
	"image"
	"image/draw"
	"log"

	"github.com/go-gl/gl/v4.1-core/gl"
	api "github.com/richinsley/goshadertoy/api"
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

	// Determine the correct internal format for the texture.
	var internalFormat int32 = gl.RGBA8
	if sampler.SRGB == "true" {
		internalFormat = gl.SRGB8_ALPHA8
		log.Printf("CubeMap Channel: Using sRGB texture format (srgb=true)")
	}

	for i := 0; i < 6; i++ {
		// Convert the input image to RGBA, which is what OpenGL expects.
		rgba := image.NewRGBA(images[i].Bounds())
		draw.Draw(rgba, rgba.Bounds(), images[i], image.Point{}, draw.Src)

		// Flip the image vertically to match OpenGL's coordinate system.
		bounds := rgba.Bounds()
		width := int32(bounds.Dx())
		height := int32(bounds.Dy())

		flipped := image.NewRGBA(bounds)
		stride := rgba.Stride // Bytes per row in the source image.

		for y := 0; y < int(height); y++ {
			srcY := y
			dstY := int(height) - 1 - y

			srcRow := rgba.Pix[srcY*stride : (srcY+1)*stride]
			dstRow := flipped.Pix[dstY*stride : (dstY+1)*stride]

			copy(dstRow, srcRow)
		}

		gl.TexImage2D(
			gl.TEXTURE_CUBE_MAP_POSITIVE_X+uint32(i),
			0,
			internalFormat,
			width,
			height,
			0,
			gl.RGBA,
			gl.UNSIGNED_BYTE,
			// Use the pixel data from the newly created 'flipped' image.
			gl.Ptr(flipped.Pix),
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

// --- IChannel Interface Implementation ---
func (c *CubeMapChannel) GetCType() string          { return c.ctype }
func (c *CubeMapChannel) Update(uniforms *Uniforms) { /* No-op for static cube maps. */ }
func (c *CubeMapChannel) GetTextureID() uint32      { return c.textureID }
func (c *CubeMapChannel) ChannelRes() [3]float32    { return c.resolution }
func (c *CubeMapChannel) Destroy()                  { gl.DeleteTextures(1, &c.textureID) }
func (c *CubeMapChannel) GetSamplerType() string    { return "samplerCube" }
