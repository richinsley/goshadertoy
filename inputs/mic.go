package inputs

import (
	"fmt"
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
	fftInputSize      = 2048
	historyBufferSize = fftInputSize * 4 // Store ample history
)

// MicChannel acts as a consumer of an audio stream.
type MicChannel struct {
	ctype         string
	textureID     uint32
	audioDevice   audio.AudioDevice
	historyBuffer []float32
	bufferPos     int
	mutex         sync.Mutex

	textureData []float32

	// For temporal smoothing
	lastFFT         []float64
	smoothingFactor float64
}

// NewMicChannel creates a channel that gets data from the default microphone.
func NewMicChannel(options *options.ShaderOptions, sampler api.Sampler) (*MicChannel, error) {
	mic, err := audio.NewFFmpegAudioDevice(options)
	if err != nil {
		log.Printf("Could not initialize microphone: %v. Using silent fallback.", err)
		return NewMicChannelWithDevice(audio.NewNullDevice(44100), options, sampler)
	}
	log.Println("Initialized MicChannel with default microphone.")
	return NewMicChannelWithDevice(mic, options, sampler)
}

// NewMicChannelWithFFmpeg creates a channel that gets data from an FFmpeg process.
func NewMicChannelWithFFmpeg(options *options.ShaderOptions, sampler api.Sampler) (*MicChannel, error) {
	device, err := audio.NewFFmpegAudioDevice(options)
	if err != nil {
		log.Printf("Could not initialize FFmpeg audio device: %v. Using silent fallback.", err)
		return NewMicChannelWithDevice(audio.NewNullDevice(44100), options, sampler)
	}
	return NewMicChannelWithDevice(device, options, sampler)
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
		historyBuffer:   make([]float32, historyBufferSize),
		textureData:     make([]float32, textureWidth*textureHeight*2),
		lastFFT:         make([]float64, textureWidth),
		smoothingFactor: 0.8,
	}

	audioChan, err := mc.audioDevice.Start()
	if err != nil {
		return nil, fmt.Errorf("could not start audio device: %w", err)
	}

	go mc.listenForAudio(audioChan)

	log.Printf("MicChannel audio listener started.")
	return mc, nil
}

// listenForAudio runs in a dedicated goroutine, consuming data from the
// audio device channel and populating the internal history buffer.
func (c *MicChannel) listenForAudio(audioChan <-chan []float32) {
	for samples := range audioChan {
		c.mutex.Lock()
		for _, sample := range samples {
			c.historyBuffer[c.bufferPos] = sample
			c.bufferPos = (c.bufferPos + 1) % historyBufferSize
		}
		c.mutex.Unlock()
	}
	log.Printf("Audio channel for mic input closed. Listener goroutine exiting.")
}

// getRecentSamples retrieves the latest samples from the internal history buffer.
func (c *MicChannel) getRecentSamples(numSamples int) []float32 {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	out := make([]float32, numSamples)
	for i := 0; i < numSamples; i++ {
		index := (c.bufferPos - numSamples + i + historyBufferSize) % historyBufferSize
		out[i] = c.historyBuffer[index]
	}
	return out
}

// Update reads from its own history buffer instead of the device directly.
func (c *MicChannel) Update(uniforms *Uniforms) {
	const minDecibels = -100.0
	const maxDecibels = -30.0

	// Get 2048 samples and apply the Blackman window.
	samples := c.getRecentSamples(fftInputSize)
	window := blackmanWindow(fftInputSize)
	samples64 := make([]float64, fftInputSize)
	for i, s := range samples {
		samples64[i] = float64(s) * window[i]
	}

	// Perform a 2048-point FFT. This gives us 1024 frequency bins.
	fftResult := fft.FFTReal(samples64)

	// --- Process FFT (Frequency) Data ---
	// We only care about the first 512 bins for our 512px texture.
	for i := 0; i < textureWidth; i++ {
		re := real(fftResult[i])
		im := imag(fftResult[i])
		// Normalize magnitude by 2.0/N for all non-DC/Nyquist components.
		magnitude := math.Sqrt(re*re+im*im) * (2.0 / float64(fftInputSize))

		// Convert to decibels.
		db := 20 * math.Log10(magnitude+1e-9)

		// Apply temporal smoothing.
		c.lastFFT[i] = (c.smoothingFactor * c.lastFFT[i]) + ((1.0 - c.smoothingFactor) * db)
		smoothedDb := c.lastFFT[i]

		// Scale to [0.0, 1.0] range.
		var scaledValue float32
		if smoothedDb < minDecibels {
			scaledValue = 0.0
		} else if smoothedDb > maxDecibels {
			scaledValue = 1.0
		} else {
			scaledValue = float32((smoothedDb - minDecibels) / (maxDecibels - minDecibels))
		}

		// First row of texture (R channel) gets frequency data.
		c.textureData[i*2] = scaledValue
		c.textureData[i*2+1] = 0.0 // G channel is unused for this row
	}

	// --- Process Waveform Data ---
	// Use the most recent 512 samples for the waveform display.
	waveSegment := samples[len(samples)-textureWidth:]
	for i := 0; i < textureWidth; i++ {
		// Second row of texture (R channel) gets waveform data.
		// We scale from [-1, 1] to [0, 1] for the texture.
		c.textureData[(textureWidth+i)*2] = (waveSegment[i] + 1.0) * 0.5
		c.textureData[(textureWidth+i)*2+1] = 0.0 // G channel is unused
	}

	// Upload the data.
	gl.BindTexture(gl.TEXTURE_2D, c.textureID)
	gl.TexSubImage2D(gl.TEXTURE_2D, 0, 0, 0, textureWidth, textureHeight, gl.RG, gl.FLOAT, gl.Ptr(c.textureData))
	gl.BindTexture(gl.TEXTURE_2D, 0)
}

// Destroy just calls Stop() on the device.
func (c *MicChannel) Destroy() {
	log.Printf("Destroying MicChannel and stopping audio device.")
	if c.audioDevice != nil {
		c.audioDevice.Stop() // This will close the channel and stop the goroutine.
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
