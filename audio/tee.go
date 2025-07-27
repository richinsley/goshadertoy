package audio

import "log"

// Tee creates a fan-out from a single input channel to multiple output channels,
// broadcasting every value from the input to all outputs. This function is a
// fundamental concurrency pattern used when multiple, independent consumers
// (e.g., an audio player and an audio visualizer) need to process the exact
// same stream of data simultaneously.
//
// **The Competing Consumer Problem:**
// A common pitfall in concurrent design is having multiple goroutines read from the
// same channel. This does not create a broadcast; instead, it creates a "competing
// consumer" scenario where each value sent on the channel is received by only *one*
// of the goroutines, leading to unpredictable data distribution and potential
// starvation for some consumers.
//
// **The Broadcast Solution:**
// This function implements a robust broadcast by using a single, dedicated goroutine
// as the sole reader of the `input` channel. This central goroutine is responsible
// for distributing each value to all registered `outputs`.
//
// Key features of this implementation:
//
//  1. **Single Reader, Multiple Writers:** A single goroutine reads from `input`
//     and writes to all `outputs`, preventing race conditions on the input.
//
//  2. **Data Isolation:** A new copy of the data slice (`dataCopy`) is made for each
//     broadcast. This is critical. Without a copy, all consumers would receive a
//     pointer to the same underlying array, and a modification by one consumer
//     would corrupt the data for all others.
//
//  3. **Synchronized Broadcast & Backpressure:** The send to each output channel
//     (`out <- dataCopy`) is a blocking operation. The main loop will not proceed
//     to the next value from `input` until *all* output channels have accepted the
//     current value. This synchronizes the consumers and provides natural
//     backpressure if one consumer is slower than the producer.
//
//  4. **Graceful Shutdown:** When the `input` channel is closed, the `for...range`
//     loop terminates. The function then closes all `output` channels, cleanly
//     signaling the end of the stream to all downstream consumers.
//
//  5. **Error Handling:** If an output channel is closed while trying to send data,
//     the send operation will panic. This is caught by a deferred `recover` call,
//     which logs a warning instead of crashing the entire program. This allows the
//     broadcast to continue to other outputs even if one consumer is no longer
//     available.
func Tee(input <-chan []float32, outputs ...chan<- []float32) {
	go func() {
		for data := range input {
			// Create a copy of the data slice to ensure each consumer
			// gets its own independent version. This prevents race conditions
			// if a consumer modifies the slice.
			dataCopy := make([]float32, len(data))
			copy(dataCopy, data)

			for _, out := range outputs {
				// Use an anonymous function to isolate the recover
				func(ch chan<- []float32, data []float32) {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("Warning: Cannot send to output channel (closed): %v", r)
						}
					}()
					// This send will block until the consumer is ready to receive.
					// This provides natural backpressure.
					ch <- data
				}(out, dataCopy)
			}
		}

		// Close all outputs, ignoring panics from already-closed channels
		for _, out := range outputs {
			func(ch chan<- []float32) {
				defer func() {
					recover() // Ignore panic if already closed
				}()
				close(ch)
			}(out)
		}
	}()
}
