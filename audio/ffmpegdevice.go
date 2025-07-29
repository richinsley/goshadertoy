// audio/ffmpegdevice.go
package audio

import (
	"log"
	"runtime"

	options "github.com/richinsley/goshadertoy/options"
)

// FFmpegDeviceInput captures audio from a live device.
type FFmpegDeviceInput struct {
	ffmpegBaseDevice
}

// NewFFmpegDeviceInput creates a new audio device that captures from a live device.
func NewFFmpegDeviceInput(options *options.ShaderOptions) (*FFmpegDeviceInput, error) {
	d := &FFmpegDeviceInput{
		ffmpegBaseDevice: ffmpegBaseDevice{
			options:  options,
			stopChan: make(chan struct{}),
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
	log.Println("Initializing FFmpeg for device input...")
	var format string
	inputOptions := map[string]string{"fflags": "nobuffer"}

	switch runtime.GOOS {
	case "darwin":
		format = "avfoundation"
	case "linux":
		format = "pulse"
	case "windows":
		format = "dshow"
	}

	// Rate emulation is never needed for live device capture.
	err := d.init(*d.options.AudioInputDevice, format, "mono", false, inputOptions)
	if err != nil {
		return nil, err
	}
	return d.start()
}
