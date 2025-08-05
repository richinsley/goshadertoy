package audio

import (
	"context"
	"time"

	options "github.com/richinsley/goshadertoy/options"
)

// audioBaseDevice contains common logic for audio devices.
type audioBaseDevice struct {
	options             *options.ShaderOptions
	buffer              *SharedAudioBuffer
	player              *AudioPlayer
	mode                string
	enableRateEmulation bool
	startTime           time.Time
	samplesSent         int64
	cancel              context.CancelFunc
	sampleRate          int
}

func (d *audioBaseDevice) GetBuffer() *SharedAudioBuffer {
	return d.buffer
}

func (d *audioBaseDevice) SampleRate() int {
	return d.sampleRate
}

func (d *audioBaseDevice) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	if d.player != nil {
		d.player.Stop()
	}
	return nil
}

// DecodeUntil is a placeholder for devices that don't support passive decoding.
func (d *audioBaseDevice) DecodeUntil(targetSample int64) error {
	return nil
}
