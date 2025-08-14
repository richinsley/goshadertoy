//go:build linux && cgo
// +build linux,cgo

package arcana

/*
#include <libavutil/samplefmt.h>
*/
import "C"
import "runtime"

// ProbeDeviceForBestFormat is a cross-platform function to find the best sample format.
// It delegates to platform-specific implementations.
func ProbeDeviceForBestFormat(deviceName string, channels, sampleRate int) (C.enum_AVSampleFormat, error) {
	if runtime.GOOS == "linux" {
		return probeAlsaDeviceForBestFormat(deviceName, channels, sampleRate)
	}
	// Default for other OSes (macOS, Windows). They are generally more flexible
	// and handle conversion internally, so defaulting to float is safe.
	return C.AV_SAMPLE_FMT_FLT, nil
}
