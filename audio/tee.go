package audio

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
func Tee(input <-chan []float32, outputs ...chan<- []float32) {
	// A single goroutine reads from the input and writes to all outputs.
	go func() {
		// When the input channel is closed, this loop will terminate.
		for data := range input {
			// Create a copy of the data slice to ensure each consumer
			// gets its own independent version. This prevents race conditions
			// if a consumer modifies the slice.
			dataCopy := make([]float32, len(data))
			copy(dataCopy, data)

			// Send the data copy to every output channel.
			for _, out := range outputs {
				// This send will block until the consumer is ready to receive.
				// This provides natural backpressure.
				out <- dataCopy
			}
		}

		// Once the input channel is closed, close all the output channels
		// to signal completion to the consumers.
		for _, out := range outputs {
			close(out)
		}
	}()
}
