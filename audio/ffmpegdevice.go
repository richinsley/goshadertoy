// audio/ffmpegdevice.go
package audio

import (
	"log"
	"runtime"

	options "github.com/richinsley/goshadertoy/options"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// FFmpegDeviceInput captures audio from a live device.
type FFmpegDeviceInput struct {
	ffmpegBaseDevice
}

// NewFFmpegDeviceInput creates a new audio device that captures from a live device.
func NewFFmpegDeviceInput(options *options.ShaderOptions) (*FFmpegDeviceInput, error) {
	d := &FFmpegDeviceInput{
		ffmpegBaseDevice: ffmpegBaseDevice{
			options:    options,
			stopChan:   make(chan struct{}),
			sampleRate: 44100, // Default sample rate
		},
	}

	if *options.AudioOutputDevice != "" {
		player, err := NewAudioPlayer(options)
		if err != nil {
			return nil, err
		}
		d.player = player
	}
	return d, nil
}

// Start configures FFmpeg to capture from a live device and starts the process.
func (d *FFmpegDeviceInput) Start() (<-chan []float32, error) {
	inputArgs := ffmpeg.KwArgs{
		"fflags": "nobuffer",
	}

	switch runtime.GOOS {
	case "darwin":
		inputArgs["f"] = "avfoundation"
	case "linux":
		inputArgs["f"] = "pulse" // or "alsa"
	case "windows":
		inputArgs["f"] = "dshow"
	}
	log.Println("Starting FFmpeg device input...")
	return d.ffmpegBaseDevice.Start(inputArgs)
}
