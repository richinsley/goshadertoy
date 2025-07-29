package audio

import "sync"

// SharedAudioBuffer provides a thread-safe, circular buffer for float32 audio samples.
type SharedAudioBuffer struct {
	mu           sync.RWMutex
	buffer       []float32
	writePos     int
	capacity     int
	totalWritten int64 // Total samples written, to calculate current time
}

func NewSharedAudioBuffer(capacity int) *SharedAudioBuffer {
	return &SharedAudioBuffer{
		buffer:   make([]float32, capacity),
		capacity: capacity,
	}
}

// Write adds new samples to the buffer, overwriting the oldest samples if necessary.
func (b *SharedAudioBuffer) Write(samples []float32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sample := range samples {
		b.buffer[b.writePos] = sample
		b.writePos = (b.writePos + 1) % b.capacity
	}
	b.totalWritten += int64(len(samples))
}

// ReadLatest retrieves the most recent `count` samples from the buffer.
func (b *SharedAudioBuffer) ReadLatest(count int) []float32 {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if count > b.capacity {
		count = b.capacity
	}

	out := make([]float32, count)
	// Start reading `count` samples behind the current write position
	readPos := (b.writePos - count + b.capacity) % b.capacity

	for i := 0; i < count; i++ {
		out[i] = b.buffer[readPos]
		readPos = (readPos + 1) % b.capacity
	}
	return out
}

// ReadFrom retrieves `count` samples starting from a specific `offset`
// behind the current write position.
func (b *SharedAudioBuffer) ReadFrom(offset int, count int) []float32 {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Ensure reads don't exceed buffer capacity
	if count > b.capacity {
		count = b.capacity
	}
	if offset > b.capacity {
		// Offset is too far in the past, return silence
		return make([]float32, count)
	}

	out := make([]float32, count)
	// Calculate the starting point for the read
	startPos := (b.writePos - offset + b.capacity) % b.capacity

	for i := 0; i < count; i++ {
		readPos := (startPos + i) % b.capacity
		out[i] = b.buffer[readPos]
	}
	return out
}

// TotalSamplesWritten returns the total number of samples that have been written to the buffer.
func (b *SharedAudioBuffer) TotalSamplesWritten() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalWritten
}
