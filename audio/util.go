package audio

// DownmixStereoToMono converts an interleaved stereo float32 buffer to mono
// by averaging the left and right channels.
func DownmixStereoToMono(stereo []float32) []float32 {
	if len(stereo)%2 != 0 {
		// Handle odd-length slices, though this shouldn't happen with stereo audio
		stereo = stereo[:len(stereo)-1]
	}
	mono := make([]float32, len(stereo)/2)
	for i := 0; i < len(mono); i++ {
		// Average left and right channels
		mono[i] = (stereo[i*2] + stereo[i*2+1]) * 0.5
	}
	return mono
}
