package audio

import (
	"fmt"
	"log"

	"github.com/gordonklaus/portaudio"
)

// Microphone now acts as a pure producer, sending data to a channel.
type Microphone struct {
	sampleRate  int
	stream      *portaudio.Stream
	audioChan   chan []float32
	isStreaming bool
}

func NewMicrophone(sampleRate int) (*Microphone, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize portaudio: %w", err)
	}
	return &Microphone{sampleRate: sampleRate}, nil
}

// audioCallback now sends data to the channel.
func (m *Microphone) audioCallback(in []float32) {
	// We must copy the input slice, as PortAudio will reuse its buffer.
	dataCopy := make([]float32, len(in))
	copy(dataCopy, in)

	// Use a non-blocking send to avoid blocking the audio callback thread
	// if the consumer is not ready.
	select {
	case m.audioChan <- dataCopy:
	default:
		log.Println("Warning: Audio channel buffer is full. Dropping audio frame.")
	}
}

func (m *Microphone) Start() (<-chan []float32, error) {
	// Create a buffered channel to handle jitter between the callback and consumer.
	m.audioChan = make(chan []float32, 16)

	host, err := portaudio.DefaultHostApi()
	if err != nil {
		close(m.audioChan)
		return nil, err
	}

	params := portaudio.HighLatencyParameters(host.DefaultInputDevice, nil)
	params.Input.Channels = 1
	params.SampleRate = float64(m.sampleRate)

	stream, err := portaudio.OpenStream(params, m.audioCallback)
	if err != nil {
		close(m.audioChan)
		return nil, fmt.Errorf("failed to open audio stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		close(m.audioChan)
		return nil, fmt.Errorf("failed to start audio stream: %w", err)
	}
	m.stream = stream
	m.isStreaming = true

	return m.audioChan, nil
}

func (m *Microphone) Stop() error {
	if !m.isStreaming {
		return nil
	}
	if err := m.stream.Close(); err != nil {
		portaudio.Terminate()
		return err
	}
	m.isStreaming = false
	close(m.audioChan) // Signal that no more data will be sent.
	return portaudio.Terminate()
}

func (m *Microphone) SampleRate() int {
	return m.sampleRate
}
