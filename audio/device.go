package audio

// We'll be using portaudio for audio input handling.
// macos:	brew install portaudio
// debian:	sudo apt-get install portaudio19-dev
// windows:	pacman -S mingw-w64-x86_64-portaudio

// A producer will implement this to provide a stream of audio sample chunks.
type AudioDevice interface {
	// Start begins audio processing and returns a receive-only channel of audio chunks.
	Start() (<-chan []float32, error)
	// Stop terminates the audio stream and closes the channel.
	Stop() error
	// SampleRate returns the sample rate of the device.
	SampleRate() int
}

// NullDevice implementation updated for the new interface.
type NullDevice struct {
	rate   int
	stopCh chan struct{}
}

func NewNullDevice(sampleRate int) *NullDevice {
	return &NullDevice{
		rate:   sampleRate,
		stopCh: make(chan struct{}),
	}
}

// Start for NullDevice produces a channel that never sends anything.
func (d *NullDevice) Start() (<-chan []float32, error) {
	// A nil channel will block forever on receive, effectively producing silence.
	return nil, nil
}

func (d *NullDevice) Stop() error {
	// No goroutine to stop, so nothing to do.
	return nil
}

func (d *NullDevice) SampleRate() int { return d.rate }
