package audio

import (
	"sync"
)

// SharedAudioBuffer provides a thread-safe, buffered audio queue.
type SharedAudioBuffer struct {
	mu               sync.RWMutex
	buffers          [][]float32
	maxBuffers       int
	totalWritten     int64
	droppedSamples   int64
	availableSamples int // This will now be updated correctly
	spaceChan        chan struct{}

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
	return &SharedAudioBuffer{
		buffers:          make([][]float32, 0, maxBuffers),
		maxBuffers:       maxBuffers,
		availableSamples: 0,
		spaceChan:        make(chan struct{}, maxBuffers),
		windowSize:       DefaultWindowSize,
		writeWindow:      make([]float32, DefaultWindowSize),
		readWindow:       make([]float32, DefaultWindowSize),
		writePos:         0,
	}
}

// Write adds new samples to the buffer queue and updates the peek window.
func (b *SharedAudioBuffer) Write(samples []float32, dropIfFull bool) {
	b.updateWindow(samples) // Update the non-destructive peek window first

	b.mu.Lock()
	defer b.mu.Unlock()

	bufferCopy := make([]float32, len(samples))
	copy(bufferCopy, samples)

	if len(b.buffers) >= b.maxBuffers {
		if !dropIfFull {
			// In a real-world scenario, we might block here using b.spaceChan,
			// but for now, we'll assume dropping is the desired behavior for overflow.
			return
		}
		// Drop the oldest buffer to make space.
		oldBuffer := b.buffers[0]
		b.buffers = b.buffers[1:]
		b.droppedSamples += int64(len(oldBuffer))
		b.availableSamples -= len(oldBuffer) // Decrement for the dropped buffer
	}

	b.buffers = append(b.buffers, bufferCopy)
	b.totalWritten += int64(len(samples))
	b.availableSamples += len(samples) // Increment by the number of new samples
}

// Read destructively reads the oldest 'count' samples from the buffer queue.
// This should be used by the primary audio consumer (player/recorder).
func (b *SharedAudioBuffer) Read(count int) []float32 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if count <= 0 || b.availableSamples == 0 {
		return nil
	}

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
			// The entire buffer was consumed, remove it from the queue.
			b.buffers = b.buffers[1:]
		} else {
			// Only part of the buffer was consumed, so we slice it to remove the read part.
			b.buffers[0] = buffer[samplesToCopy:]
		}
	}

	b.availableSamples -= count // Decrement by the exact number of samples read
	return out
}

// AvailableSamples returns the total number of readable samples. It is now O(1).
func (b *SharedAudioBuffer) AvailableSamples() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
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
			// When the write window is full, swap it with the read window.
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
	b.mu.RLock()
	defer b.mu.RUnlock()
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
