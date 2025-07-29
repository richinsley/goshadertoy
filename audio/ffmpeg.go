// audio/ffmpeg.go
package audio

import (
	options "github.com/richinsley/goshadertoy/options"
)

// NewFFmpegAudioDevice is a factory function that creates the appropriate audio device
// based on the provided options. It will return a device for file input, live device input,
// or a null device if no audio input is specified.
func NewFFmpegAudioDevice(options *options.ShaderOptions) (AudioDevice, error) {
	buffer := NewSharedAudioBuffer(44100 * 5) // 5-second buffer

	if options.AudioInputDevice != nil && *options.AudioInputDevice != "" {
		// User wants to capture from a live device.
		return NewFFmpegDeviceInput(options, buffer)
	}

	if options.AudioInputFile != nil && *options.AudioInputFile != "" {
		// User wants to read from a file.
		return NewFFmpegFileInput(options, buffer)
	}

	// If no specific audio input is given, we can default to a silent NullDevice.
	// This prevents errors when the user runs the program without audio flags.
	return NewNullDevice(44100), nil
}
