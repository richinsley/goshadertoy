// audio/ffmpegfile.go
package audio

import (
	"log"

	options "github.com/richinsley/goshadertoy/options"
)

// FFmpegFileInput reads audio from a file.
type FFmpegFileInput struct {
	ffmpegBaseDevice
}

// NewFFmpegFileInput creates a new audio device that reads from a file.
func NewFFmpegFileInput(options *options.ShaderOptions, buffer *SharedAudioBuffer) (*FFmpegFileInput, error) {
	d := &FFmpegFileInput{
		ffmpegBaseDevice: ffmpegBaseDevice{
			options: options,
			buffer:  buffer,
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
func (d *FFmpegFileInput) Start() error {
	log.Println("Initializing FFmpeg for file input...")

	// Rate emulation should only be enabled when treating the file as a live source.
	// For "record" mode, we want to process as fast as possible.
	enableRateEmulation := (*d.options.Mode == "live" || *d.options.Mode == "stream")
	if enableRateEmulation {
		log.Println("Rate emulation enabled for file input.")
	}

	// For file inputs, we don't need any special options like "re" anymore.
	inputOptions := make(map[string]string)

	err := d.init(*d.options.AudioInputFile, "", "stereo", enableRateEmulation, inputOptions)
	if err != nil {
		return err
	}
	return d.ffmpegBaseDevice.Start()
}
