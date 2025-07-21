package audio

import "sync"

// Tee creates a fan-out from an input channel to multiple output channels.
func Tee(input <-chan []float32, outputs ...chan<- []float32) {
	var wg sync.WaitGroup
	wg.Add(len(outputs))

	for _, output := range outputs {
		go func(out chan<- []float32) {
			defer wg.Done()
			for data := range input {
				out <- data
			}
		}(output)
	}

	go func() {
		wg.Wait()
		for _, output := range outputs {
			close(output)
		}
	}()
}
