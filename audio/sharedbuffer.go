package audio

import (
	"sync"
)

// SharedAudioBuffer now uses a queue of buffers internally but maintains the same API
type SharedAudioBuffer struct {
	mu             sync.RWMutex
	buffers        [][]float32
	maxBuffers     int
	totalWritten   int64
	droppedSamples int64
	currentReadBuf []float32 // Current buffer being read from
	readOffset     int       // Offset within current read buffer
}

func NewSharedAudioBuffer(capacity int) *SharedAudioBuffer {
	// Convert capacity to number of buffers (assuming ~1024 samples per buffer)
	// You can adjust this ratio based on your typical buffer sizes
	maxBuffers := max(capacity/1024, 10)

	return &SharedAudioBuffer{
		buffers:    make([][]float32, 0, maxBuffers),
		maxBuffers: maxBuffers,
	}
}

// Write adds new samples to the buffer queue, dropping oldest if necessary
func (b *SharedAudioBuffer) Write(samples []float32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Make a copy to avoid external modifications
	bufferCopy := make([]float32, len(samples))
	copy(bufferCopy, samples)

	// If queue is full, drop the oldest buffer
	if len(b.buffers) >= b.maxBuffers {
		oldBuffer := b.buffers[0]
		b.buffers = b.buffers[1:]
		b.droppedSamples += int64(len(oldBuffer))

		// If we were reading from the dropped buffer, reset read state
		if len(b.currentReadBuf) > 0 && &b.currentReadBuf[0] == &oldBuffer[0] {
			b.currentReadBuf = nil
			b.readOffset = 0
		}
	}

	b.buffers = append(b.buffers, bufferCopy)
	b.totalWritten += int64(len(samples))
}

// ReadLatest retrieves the most recent `count` samples from the buffer.
func (b *SharedAudioBuffer) ReadLatest(count int) []float32 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if count <= 0 {
		return nil
	}

	out := make([]float32, count)
	outPos := 0

	// Calculate total available samples
	totalAvailable := b.getTotalAvailableSamples()
	if totalAvailable == 0 {
		return out // Return silence if no data
	}

	// If we want more than available, adjust count
	if count > totalAvailable {
		count = totalAvailable
		out = out[:count]
	}

	// Start from the end and work backwards to get the "latest" samples
	samplesNeeded := count
	bufferIdx := len(b.buffers) - 1

	// Collect samples from newest to oldest buffers
	tempBuffers := make([][]float32, 0, len(b.buffers))
	for bufferIdx >= 0 && samplesNeeded > 0 {
		buffer := b.buffers[bufferIdx]
		if len(buffer) <= samplesNeeded {
			// Take the whole buffer
			tempBuffers = append(tempBuffers, buffer)
			samplesNeeded -= len(buffer)
		} else {
			// Take only the last part of this buffer
			start := len(buffer) - samplesNeeded
			tempBuffers = append(tempBuffers, buffer[start:])
			samplesNeeded = 0
		}
		bufferIdx--
	}

	// Reverse the order and copy to output (since we collected backwards)
	for i := len(tempBuffers) - 1; i >= 0; i-- {
		copy(out[outPos:], tempBuffers[i])
		outPos += len(tempBuffers[i])
	}

	return out
}

// ReadFrom retrieves `count` samples starting from a specific `offset` behind the current write position
func (b *SharedAudioBuffer) ReadFrom(offset int, count int) []float32 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if count <= 0 {
		return nil
	}

	out := make([]float32, count)
	totalAvailable := b.getTotalAvailableSamples()

	if offset >= totalAvailable {
		return out // Return silence if offset is too far back
	}

	// Calculate the actual starting position
	startPos := totalAvailable - offset
	if startPos < 0 {
		startPos = 0
	}

	// Adjust count if we don't have enough samples
	availableFromStart := totalAvailable - startPos
	if count > availableFromStart {
		count = availableFromStart
		out = out[:count]
	}

	// Find which buffer contains our start position
	samplesSkipped := 0
	bufferIdx := 0
	offsetInBuffer := 0

	for bufferIdx < len(b.buffers) {
		bufferLen := len(b.buffers[bufferIdx])
		if samplesSkipped+bufferLen > startPos {
			offsetInBuffer = startPos - samplesSkipped
			break
		}
		samplesSkipped += bufferLen
		bufferIdx++
	}

	// Copy samples starting from the calculated position
	outPos := 0
	samplesRemaining := count

	for bufferIdx < len(b.buffers) && samplesRemaining > 0 {
		buffer := b.buffers[bufferIdx]
		availableInBuffer := len(buffer) - offsetInBuffer
		samplesToCopy := min(samplesRemaining, availableInBuffer)

		copy(out[outPos:], buffer[offsetInBuffer:offsetInBuffer+samplesToCopy])
		outPos += samplesToCopy
		samplesRemaining -= samplesToCopy

		bufferIdx++
		offsetInBuffer = 0 // Only first buffer might have an offset
	}

	return out
}

// TotalSamplesWritten returns the total number of samples that have been written to the buffer
func (b *SharedAudioBuffer) TotalSamplesWritten() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalWritten
}

// Additional methods for monitoring (optional, but useful)

// QueueLength returns the number of buffers currently queued
func (b *SharedAudioBuffer) QueueLength() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.buffers)
}

// DroppedSamples returns the number of samples that have been dropped due to buffer overflow
func (b *SharedAudioBuffer) DroppedSamples() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.droppedSamples
}

// AvailableSamples returns the total number of samples currently available for reading
func (b *SharedAudioBuffer) AvailableSamples() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.getTotalAvailableSamples()
}

// Helper method to calculate total available samples (must be called with lock held)
func (b *SharedAudioBuffer) getTotalAvailableSamples() int {
	total := 0
	for _, buffer := range b.buffers {
		total += len(buffer)
	}
	return total
}

// Helper function
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
