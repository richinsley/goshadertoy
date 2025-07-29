package inputs

import (
	"log"
	"math"
	"sync"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	fft "github.com/mjibson/go-dsp/fft"
	api "github.com/richinsley/goshadertoy/api"
	audio "github.com/richinsley/goshadertoy/audio"
	options "github.com/richinsley/goshadertoy/options"
)

const (
	textureWidth  = 512
	textureHeight = 2
	// Shadertoy uses an fftSize of 2048, which gives 1024 frequency bins.
	fftInputSize = 2048
)

// MicChannel acts as a consumer of an audio stream.
type MicChannel struct {
	ctype           string
	textureID       uint32
	audioDevice     audio.AudioDevice
	textureData     []float32 // This now holds the result of the last FFT
	mode            string
	lastFFT         []float64
	smoothingFactor float64
	dataMutex       sync.Mutex // Mutex to protect textureData between processing and uploading
}

// NewMicChannel creates a channel that gets data from the default microphone.
func NewMicChannel(options *options.ShaderOptions, sampler api.Sampler, ad audio.AudioDevice) (*MicChannel, error) {
	return NewMicChannelWithDevice(ad, options, sampler)
}

func NewMicChannelWithDevice(device audio.AudioDevice, options *options.ShaderOptions, sampler api.Sampler) (*MicChannel, error) {
	var textureID uint32
	gl.GenTextures(1, &textureID)
	gl.BindTexture(gl.TEXTURE_2D, textureID)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RG32F, textureWidth, textureHeight, 0, gl.RG, gl.FLOAT, nil)
	minFilter, magFilter := getFilterMode(sampler.Filter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, minFilter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, magFilter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, getWrapMode(sampler.Wrap))
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, getWrapMode(sampler.Wrap))
	gl.BindTexture(gl.TEXTURE_2D, 0)

	mc := &MicChannel{
		ctype:           "mic",
		textureID:       textureID,
		audioDevice:     device,
		textureData:     make([]float32, textureWidth*textureHeight*2),
		lastFFT:         make([]float64, textureWidth),
		smoothingFactor: 0.8,
		mode:            *options.Mode,
	}

	log.Printf("MicChannel configured with audio device.")
	return mc, nil
}

// ProcessAudio performs the FFT on the provided mono samples and stores the
// result in the channel's internal textureData buffer. This should be called
// from the main render thread before Update.
func (c *MicChannel) ProcessAudio(monoSamples []float32) {
	const minDecibels = -100.0
	const maxDecibels = -30.0

	// Ensure we have enough samples for the FFT, pad with silence if necessary.
	if len(monoSamples) < fftInputSize {
		paddedSamples := make([]float32, fftInputSize)
		copy(paddedSamples, monoSamples)
		monoSamples = paddedSamples
	}

	// Use the most recent samples for the FFT
	fftSamples := monoSamples[len(monoSamples)-fftInputSize:]

	window := blackmanWindow(fftInputSize)
	samples64 := make([]float64, fftInputSize)
	for i, s := range fftSamples {
		samples64[i] = float64(s) * window[i]
	}

	fftResult := fft.FFTReal(samples64)

	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	// --- Process FFT (Frequency) Data ---
	for i := 0; i < textureWidth; i++ {
		re := real(fftResult[i])
		im := imag(fftResult[i])
		magnitude := math.Sqrt(re*re+im*im) * (2.0 / float64(fftInputSize))
		db := 20 * math.Log10(magnitude+1e-9)
		c.lastFFT[i] = (c.smoothingFactor * c.lastFFT[i]) + ((1.0 - c.smoothingFactor) * db)
		smoothedDb := c.lastFFT[i]

		var scaledValue float32
		if smoothedDb < minDecibels {
			scaledValue = 0.0
		} else if smoothedDb > maxDecibels {
			scaledValue = 1.0
		} else {
			scaledValue = float32((smoothedDb - minDecibels) / (maxDecibels - minDecibels))
		}

		c.textureData[i*2] = scaledValue
		c.textureData[i*2+1] = 0.0
	}

	// --- Process Waveform Data ---
	waveSegment := monoSamples[len(monoSamples)-textureWidth:]
	for i := 0; i < textureWidth; i++ {
		c.textureData[(textureWidth+i)*2] = (waveSegment[i] + 1.0) * 0.5
		c.textureData[(textureWidth+i)*2+1] = 0.0
	}
}

// Update reads from the shared buffer for FFT analysis.
func (c *MicChannel) Update(uniforms *Uniforms) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	gl.BindTexture(gl.TEXTURE_2D, c.textureID)
	gl.TexSubImage2D(gl.TEXTURE_2D, 0, 0, 0, textureWidth, textureHeight, gl.RG, gl.FLOAT, gl.Ptr(c.textureData))
	gl.BindTexture(gl.TEXTURE_2D, 0)
}

// Destroy just calls Stop() on the device.
func (c *MicChannel) Destroy() {
	log.Printf("Destroying MicChannel and stopping audio device.")
	if c.audioDevice != nil {
		c.audioDevice.Stop()
	}
	gl.DeleteTextures(1, &c.textureID)
}

// --- IChannel Interface Implementation ---
func (c *MicChannel) GetCType() string       { return c.ctype }
func (c *MicChannel) GetTextureID() uint32   { return c.textureID }
func (c *MicChannel) GetSamplerType() string { return "sampler2D" }
func (c *MicChannel) ChannelRes() [3]float32 {
	return [3]float32{float32(textureWidth), float32(textureHeight), 0}
}

// blackmanWindow generates a Blackman window, as used by Shadertoy.
func blackmanWindow(size int) []float64 {
	window := make([]float64, size)
	a0 := 0.42
	a1 := 0.5
	a2 := 0.08
	invSize := 1.0 / float64(size-1)
	for i := range window {
		t := float64(i) * invSize
		window[i] = a0 - (a1 * math.Cos(2*math.Pi*t)) + (a2 * math.Cos(4*math.Pi*t))
	}
	return window
}

// SampleRate returns the sample rate of the audio device.
func (c *MicChannel) SampleRate() int {
	return c.audioDevice.SampleRate()
}
