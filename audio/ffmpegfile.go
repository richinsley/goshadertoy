// audio/ffmpegfile.go
package audio

import (
	"log"

	options "github.com/richinsley/goshadertoy/options"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// FFmpegFileInput reads audio from a file.
type FFmpegFileInput struct {
	ffmpegBaseDevice
}

// NewFFmpegFileInput creates a new audio device that reads from a file.
func NewFFmpegFileInput(options *options.ShaderOptions) (*FFmpegFileInput, error) {
	d := &FFmpegFileInput{
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

// Start configures FFmpeg to read from a file and starts the audio capture.
func (d *FFmpegFileInput) Start() (<-chan []float32, error) {
	inputArgs := ffmpeg.KwArgs{}
	if *d.options.Mode == "stream" || *d.options.Mode == "live" {
		inputArgs["re"] = ""
	}
	log.Println("Starting FFmpeg file input...")
	return d.ffmpegBaseDevice.Start(inputArgs)
}
