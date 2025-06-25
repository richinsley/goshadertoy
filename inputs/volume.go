package inputs

import (
	"fmt"
	"log"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy"
)

// VolumeChannel represents a 3D volume texture input.
type VolumeChannel struct {
	index      int
	ctype      string
	textureID  uint32
	resolution [3]float32
	sampler    goshadertoy.Sampler
}

// NewVolumeChannel creates and initializes a new OpenGL 3D texture from parsed .bin volume data.
func NewVolumeChannel(index int, vol *goshadertoy.VolumeData, sampler goshadertoy.Sampler) (*VolumeChannel, error) {
	if vol == nil || vol.Data == nil {
		return nil, fmt.Errorf("input volume data for channel %d is nil", index)
	}

	var textureID uint32
	gl.GenTextures(1, &textureID)
	gl.BindTexture(gl.TEXTURE_3D, textureID)

	// Set texture parameters (wrapping and filtering).
	// Note the addition of TEXTURE_WRAP_R for the z-axis (depth).
	wrapMode := getWrapMode(sampler.Wrap)
	gl.TexParameteri(gl.TEXTURE_3D, gl.TEXTURE_WRAP_S, wrapMode)
	gl.TexParameteri(gl.TEXTURE_3D, gl.TEXTURE_WRAP_T, wrapMode)
	gl.TexParameteri(gl.TEXTURE_3D, gl.TEXTURE_WRAP_R, wrapMode)

	minFilter, magFilter := getFilterMode(sampler.Filter)
	gl.TexParameteri(gl.TEXTURE_3D, gl.TEXTURE_MIN_FILTER, minFilter)
	gl.TexParameteri(gl.TEXTURE_3D, gl.TEXTURE_MAG_FILTER, magFilter)

	// Determine the correct OpenGL formats from the volume metadata.
	internalFormat, format, typ, err := getVolumeFormat(vol.NumChannels, vol.Format)
	if err != nil {
		gl.DeleteTextures(1, &textureID)
		return nil, fmt.Errorf("channel %d: %w", index, err)
	}

	log.Printf("Channel %d (Volume): Uploading %dx%dx%d texture. InternalFormat: 0x%X, Format: 0x%X, Type: 0x%X",
		index, vol.Width, vol.Height, vol.Depth, internalFormat, format, typ)

	// Upload the 3D texture data to the GPU.
	gl.TexImage3D(
		gl.TEXTURE_3D,
		0, // level
		internalFormat,
		int32(vol.Width),
		int32(vol.Height),
		int32(vol.Depth),
		0, // border, must be 0
		format,
		typ,
		gl.Ptr(vol.Data),
	)

	// Generate mipmaps if the filter requires it.
	if sampler.Filter == "mipmap" {
		gl.GenerateMipmap(gl.TEXTURE_3D)
	}

	gl.BindTexture(gl.TEXTURE_3D, 0) // Unbind

	return &VolumeChannel{
		index:     index,
		ctype:     "volume",
		textureID: textureID,
		resolution: [3]float32{
			float32(vol.Width),
			float32(vol.Height),
			float32(vol.Depth),
		},
		sampler: sampler,
	}, nil
}

// getVolumeFormat translates Shadertoy's .bin format codes into OpenGL constants.
func getVolumeFormat(numChannels uint8, binFormat uint16) (internalFormat int32, format uint32, typ uint32, err error) {
	// Determine the data type (gl.FLOAT or gl.UNSIGNED_BYTE)
	switch binFormat {
	case 0:
		typ = gl.UNSIGNED_BYTE // 8-bit integer
	case 10:
		typ = gl.FLOAT // 32-bit float
	default:
		err = fmt.Errorf("unsupported volume binary format code: %d", binFormat)
		return
	}

	// Determine the internal and pixel formats based on the number of channels.
	if typ == gl.UNSIGNED_BYTE {
		switch numChannels {
		case 1:
			internalFormat, format = gl.R8, gl.RED
		case 2:
			internalFormat, format = gl.RG8, gl.RG
		case 3:
			internalFormat, format = gl.RGB8, gl.RGB
		case 4:
			internalFormat, format = gl.RGBA8, gl.RGBA
		default:
			err = fmt.Errorf("unsupported channel count for 8-bit volume: %d", numChannels)
		}
	} else { // typ == gl.FLOAT
		switch numChannels {
		case 1:
			internalFormat, format = gl.R32F, gl.RED
		case 2:
			internalFormat, format = gl.RG32F, gl.RG
		case 3:
			internalFormat, format = gl.RGB32F, gl.RGB
		case 4:
			internalFormat, format = gl.RGBA32F, gl.RGBA
		default:
			err = fmt.Errorf("unsupported channel count for float volume: %d", numChannels)
		}
	}
	return
}

// --- IChannel Interface Implementation ---
func (c *VolumeChannel) GetInputIndex() int        { return c.index }
func (c *VolumeChannel) GetCType() string          { return c.ctype }
func (c *VolumeChannel) Update(uniforms *Uniforms) { /* No-op for static volumes */ }
func (c *VolumeChannel) GetTextureID() uint32      { return c.textureID }
func (c *VolumeChannel) ChannelRes() [3]float32    { return c.resolution }
func (c *VolumeChannel) Destroy()                  { gl.DeleteTextures(1, &c.textureID) }
func (c *VolumeChannel) GetSamplerType() string    { return "sampler3D" }
