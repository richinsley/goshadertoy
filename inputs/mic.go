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
	textureWidth      = 512
	textureHeight     = 2
	fftInputSize      = textureWidth * 2
	historyBufferSize = fftInputSize * 4 // Store more history than needed for one frame
)

// MicChannel acts as a consumer of an audio stream.
type MicChannel struct {
	ctype       string
	textureID   uint32
	audioDevice audio.AudioDevice

	// Internal buffer to store recent audio history for FFT processing.
	// This is the consumer's responsibility.
	historyBuffer []float32
	bufferPos     int
	mutex         sync.Mutex

	textureData []float32
}

// NewMicChannel creates a channel that gets data from the default microphone using portaudio.
func NewMicChannel(options *options.ShaderOptions, sampler api.Sampler) (*MicChannel, error) {
	mic, err := audio.NewFFmpegAudioDevice(options)
	if err != nil {
		log.Printf("Could not initialize microphone: %v. Using silent fallback.", err)
		return NewMicChannelWithDevice(audio.NewNullDevice(44100), options, sampler)
	}
	log.Println("Initialized MicChannel with default PortAudio microphone.")
	return NewMicChannelWithDevice(mic, options, sampler)
}

// NewMicChannelWithFFmpeg creates a channel that gets data from an FFmpeg process.
func NewMicChannelWithFFmpeg(options *options.ShaderOptions, sampler api.Sampler) (*MicChannel, error) {
	device, err := audio.NewFFmpegAudioDevice(options)
	if err != nil {
		log.Printf("Could not initialize FFmpeg audio device: %v. Using silent fallback.", err)
		return NewMicChannelWithDevice(audio.NewNullDevice(44100), options, sampler)
	}
	// device.Start()
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
		ctype:         "mic",
		textureID:     textureID,
		audioDevice:   device,
		historyBuffer: make([]float32, historyBufferSize),
		textureData:   make([]float32, textureWidth*textureHeight*2),
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
	// Get recent audio samples from our internal history buffer.
	samples := c.getRecentSamples(fftInputSize)
	window := hanningWindow(fftInputSize)
	samples64 := make([]float64, fftInputSize)
	for i, s := range samples {
		samples64[i] = float64(s) * window[i]
	}
	fftResult := fft.FFTReal(samples64)
	fftGain := float32(0.6)
	waveSegment := samples[len(samples)-textureWidth:]
	for i := 0; i < textureWidth; i++ {
		fftMag := float32(math.Sqrt(real(fftResult[i])*real(fftResult[i])+imag(fftResult[i])*imag(fftResult[i]))) * fftGain
		if fftMag > 1.0 {
			fftMag = 1.0
		}
		c.textureData[i*2] = fftMag
		c.textureData[i*2+1] = 0.0
		waveVal := (waveSegment[i] + 1.0) * 0.5
		if waveVal > 1.0 {
			waveVal = 1.0
		}
		if waveVal < 0.0 {
			waveVal = 0.0
		}
		c.textureData[(textureWidth+i)*2] = waveVal
		c.textureData[(textureWidth+i)*2+1] = 0.0
	}
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
func hanningWindow(size int) []float64 {
	window := make([]float64, size)
	for i := range window {
		window[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(size-1)))
	}
	return window
}

// SampleRate returns the sample rate of the audio device.
func (c *MicChannel) SampleRate() int {
	return c.audioDevice.SampleRate()
}
