package audio

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	options "github.com/richinsley/goshadertoy/options"
)

// ShaderAudioDevice consumes audio from a sound shader and provides it as an AudioDevice.
type ShaderAudioDevice struct {
	audioBaseDevice
	preRenderedChan <-chan []float32
	decodeLock      sync.Mutex // Protects the on-demand decoding process
	samplesWritten  int64      // Correctly track stereo samples written for record mode
}

// NewShaderAudioDevice creates a new audio device that consumes from a sound shader.
func NewShaderAudioDevice(opts *options.ShaderOptions, preRenderedChan <-chan []float32, sampleRate int) (*ShaderAudioDevice, error) {
	d := &ShaderAudioDevice{
		audioBaseDevice: audioBaseDevice{
			options:    opts,
			buffer:     NewSharedAudioBuffer(sampleRate * 10),
			sampleRate: sampleRate,
		},
		preRenderedChan: preRenderedChan,
	}
	d.mode = *d.options.Mode
	d.enableRateEmulation = (*d.options.Mode == "live" || *d.options.Mode == "stream")

	if *opts.AudioOutputDevice != "" {
		player, err := NewAudioPlayer(opts)
		if err != nil {
			return nil, err
		}
		d.player = player
	}
	return d, nil
}

// Start begins the audio processing loop only for live/stream modes.
func (d *ShaderAudioDevice) Start() error {
	var ctx context.Context
	ctx, d.cancel = context.WithCancel(context.Background())

	if d.mode == "live" || d.mode == "stream" {
		go d.runLoop(ctx)
	}

	if d.player != nil {
		d.player.Start(d.buffer)
	}
	return nil
}

// DecodeUntil pulls audio from the sound renderer on-demand. This is a blocking call
// used in 'record' mode to ensure perfect synchronization between video frames and audio samples.
func (d *ShaderAudioDevice) DecodeUntil(targetSample int64) error {
	d.decodeLock.Lock()
	defer d.decodeLock.Unlock()

	const playbackChunkSize = 1024 * 2

	// This loop now correctly uses the device's own sample counter. It will
	// block and wait for new audio from the renderer whenever the current
	// number of processed samples is less than the target required by the video frame.
	for d.samplesWritten < targetSample {
		largeBuffer, ok := <-d.preRenderedChan
		if !ok {
			return fmt.Errorf("shader audio channel closed unexpectedly while decoding")
		}

		for i := 0; i < len(largeBuffer); i += playbackChunkSize {
			end := i + playbackChunkSize
			if end > len(largeBuffer) {
				end = len(largeBuffer)
			}
			chunk := largeBuffer[i:end]
			d.buffer.Write(chunk, false)
			// Increment the device's own counter by the number of stereo samples.
			d.samplesWritten += int64(len(chunk) / 2)
		}
	}

	return nil
}

// runLoop is the active processing loop for live/stream modes.
func (d *ShaderAudioDevice) runLoop(ctx context.Context) {
	d.startTime = time.Now()
	const playbackChunkSize = 1024 * 2

	for {
		select {
		case largeBuffer, ok := <-d.preRenderedChan:
			if !ok {
				log.Println("Shader audio channel closed, stopping device.")
				return
			}

			for i := 0; i < len(largeBuffer); i += playbackChunkSize {
				end := i + playbackChunkSize
				if end > len(largeBuffer) {
					end = len(largeBuffer)
				}
				chunk := largeBuffer[i:end]
				d.buffer.Write(chunk, false)
				d.samplesSent += int64(len(chunk) / 2) // For rate emulation

				if d.enableRateEmulation {
					elapsed := time.Since(d.startTime)
					expectedSamples := int64(elapsed.Seconds() * float64(d.sampleRate))
					if d.samplesSent > expectedSamples {
						aheadSamples := d.samplesSent - expectedSamples
						sleepDuration := time.Duration(float64(aheadSamples)*1e9/float64(d.sampleRate)) * time.Nanosecond
						time.Sleep(sleepDuration)
					}
				}
			}

		case <-ctx.Done():
			log.Println("Stopping shader audio device.")
			return
		}
	}
}
