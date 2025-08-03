package audio

import (
	"sync"
)

// SharedAudioBuffer provides a thread-safe, buffered audio queue.
type SharedAudioBuffer struct {
	mu               sync.Mutex // A regular Mutex is sufficient with Cond
	cond             *sync.Cond // Use a condition variable for signaling
	buffers          [][]float32
	maxBuffers       int
	totalWritten     int64
	droppedSamples   int64
	availableSamples int

	// Window for non-destructive peeking (for FFT)
	windowMu    sync.RWMutex
	windowSize  int
	writeWindow []float32
	readWindow  []float32
	writePos    int
}

const DefaultWindowSize = 2048

// NewSharedAudioBuffer creates a new buffer.
func NewSharedAudioBuffer(capacity int) *SharedAudioBuffer {
	maxBuffers := max(capacity/1024, 20)
	b := &SharedAudioBuffer{
		buffers:          make([][]float32, 0, maxBuffers),
		maxBuffers:       maxBuffers,
		availableSamples: 0,
		windowSize:       DefaultWindowSize,
		writeWindow:      make([]float32, DefaultWindowSize),
		readWindow:       make([]float32, DefaultWindowSize),
		writePos:         0,
	}
	// Initialize the condition variable with the Mutex
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Write adds new samples to the buffer.
// If dropIfFull is true, it drops the oldest samples if the buffer is full.
// If dropIfFull is false, it blocks until space is available.
func (b *SharedAudioBuffer) Write(samples []float32, dropIfFull bool) {
	b.updateWindow(samples) // Update the non-destructive peek window first

	b.mu.Lock()
	defer b.mu.Unlock()

	// If we are in blocking mode, wait for space.
	if !dropIfFull {
		for len(b.buffers) >= b.maxBuffers {
			b.cond.Wait() // This atomically unlocks mu and waits.
		}
	}

	// Handle buffer being full for the dropping case.
	if len(b.buffers) >= b.maxBuffers {
		if dropIfFull {
			// Drop the oldest buffer to make space.
			oldBuffer := b.buffers[0]
			b.buffers = b.buffers[1:]
			b.droppedSamples += int64(len(oldBuffer))
			b.availableSamples -= len(oldBuffer)
		} else {
			// This case should not be reached if blocking is working correctly,
			// but as a safeguard, we return.
			return
		}
	}

	bufferCopy := make([]float32, len(samples))
	copy(bufferCopy, samples)

	b.buffers = append(b.buffers, bufferCopy)
	b.totalWritten += int64(len(samples))
	b.availableSamples += len(samples)
}

// Read destructively reads the oldest 'count' samples from the buffer queue.
func (b *SharedAudioBuffer) Read(count int) []float32 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if count <= 0 || b.availableSamples == 0 {
		return nil
	}

	// Check if we were previously at full capacity.
	wasFull := len(b.buffers) >= b.maxBuffers

	if count > b.availableSamples {
		count = b.availableSamples
	}

	out := make([]float32, count)
	outPos := 0
	samplesRemainingToRead := count

	// Consume from the oldest buffers first (FIFO)
	for len(b.buffers) > 0 && samplesRemainingToRead > 0 {
		buffer := b.buffers[0]
		samplesToCopy := min(samplesRemainingToRead, len(buffer))

		copy(out[outPos:], buffer[:samplesToCopy])

		outPos += samplesToCopy
		samplesRemainingToRead -= samplesToCopy

		if samplesToCopy == len(buffer) {
			b.buffers = b.buffers[1:]
		} else {
			b.buffers[0] = buffer[samplesToCopy:]
		}
	}

	b.availableSamples -= count

	// If the buffer was full and we've now made space, signal a waiting writer.
	if wasFull && len(b.buffers) < b.maxBuffers {
		b.cond.Signal()
	}

	return out
}

// AvailableSamples returns the total number of readable samples.
func (b *SharedAudioBuffer) AvailableSamples() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.availableSamples
}

// --- Window (Peek) Functionality ---

func (b *SharedAudioBuffer) updateWindow(samples []float32) {
	b.windowMu.Lock()
	defer b.windowMu.Unlock()

	sampleIdx := 0
	for sampleIdx < len(samples) {
		spaceInWindow := b.windowSize - b.writePos
		samplesToWrite := min(len(samples)-sampleIdx, spaceInWindow)

		copy(b.writeWindow[b.writePos:b.writePos+samplesToWrite], samples[sampleIdx:sampleIdx+samplesToWrite])
		b.writePos += samplesToWrite
		sampleIdx += samplesToWrite

		if b.writePos >= b.windowSize {
			b.writeWindow, b.readWindow = b.readWindow, b.writeWindow
			b.writePos = 0
		}
	}
}

// WindowPeek returns a copy of the most recent audio data for FFT analysis.
func (b *SharedAudioBuffer) WindowPeek() []float32 {
	b.windowMu.RLock()
	defer b.windowMu.RUnlock()
	result := make([]float32, b.windowSize)
	copy(result, b.readWindow)
	return result
}

// --- Helper functions and other accessors ---

func (b *SharedAudioBuffer) TotalSamplesWritten() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalWritten
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
