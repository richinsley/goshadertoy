package audio

// We'll be using portaudio for audio input handling.
// macos:	brew install portaudio
// debian:	sudo apt-get install portaudio19-dev
// windows:	pacman -S mingw-w64-x86_64-portaudio

// A producer will implement this to provide a stream of audio sample chunks.
type AudioDevice interface {
	// Start begins audio processing.
	Start() error
	// Stop terminates the audio stream.
	Stop() error
	// SampleRate returns the sample rate of the device.
	SampleRate() int
	// GetBuffer returns the shared audio buffer.
	GetBuffer() *SharedAudioBuffer
	// DecodeUntil decodes the audio source until the given sample count is reached.
	// This is a no-op for live devices and used for file-based sources in record mode.
	DecodeUntil(targetSample int64) error
}

// NullDevice implementation updated for the new interface.
type NullDevice struct {
	rate   int
	stopCh chan struct{}
	buffer *SharedAudioBuffer
}

func NewNullDevice(sampleRate int) *NullDevice {
	return &NullDevice{
		rate:   sampleRate,
		stopCh: make(chan struct{}),
		buffer: NewSharedAudioBuffer(sampleRate * 5), // 5 seconds of buffer
	}
}

func (d *NullDevice) DecodeUntil(targetSample int64) error {
	return nil // Null device does nothing
}

// Start for NullDevice produces a channel that never sends anything.
func (d *NullDevice) Start() error {
	return nil
}

func (d *NullDevice) Stop() error {
	return nil
}

func (d *NullDevice) SampleRate() int {
	return d.rate
}

func (d *NullDevice) GetBuffer() *SharedAudioBuffer {
	return d.buffer
}
