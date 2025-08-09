//go:build linux && cgo
// +build linux,cgo

package arcana

/*
#cgo CFLAGS: -I${SRCDIR}/../release/include/arcana
#cgo LDFLAGS: -L${SRCDIR}/../release/lib -lasound
#cgo pkg-config: --static libavutil_arcana libswresample_arcana libavcodec_arcana libavformat_arcana libswscale_arcana libavfilter_arcana libavdevice_arcana libpostproc_arcana x264 x265

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavfilter/avfilter.h>
#include <libavdevice/avdevice.h>
#include <libpostproc/postprocess.h>
#include <libswresample/swresample.h>
#include <libswscale/swscale.h>
#include <stdio.h>
#include <alsa/asoundlib.h>


// A simple C log callback that prints directly to stderr.
static void simple_log_callback(void* ptr, int level, const char* fmt, va_list vl) {
    if (level > AV_LOG_DEBUG) {
        return;
    }
    fprintf(stderr, "[FFmpeg] ");
    vfprintf(stderr, fmt, vl);
}

// Function to set the callback
static void set_log_callback() {
    av_log_set_callback(simple_log_callback);
}

// Helper to convert FFmpeg AVSampleFormat to ALSA snd_pcm_format_t
static snd_pcm_format_t av_to_alsa_format(enum AVSampleFormat fmt) {
    switch (fmt) {
        case AV_SAMPLE_FMT_S16:  return SND_PCM_FORMAT_S16_LE;
        case AV_SAMPLE_FMT_S32:  return SND_PCM_FORMAT_S32_LE;
        case AV_SAMPLE_FMT_FLT:  return SND_PCM_FORMAT_FLOAT_LE;
        default:                 return SND_PCM_FORMAT_UNKNOWN;
    }
}
*/
import "C"
import (
	"fmt"
	"log"
	"unsafe"
)

func Platform_init() {
	C.av_log_set_level(C.AV_LOG_INFO)
	C.set_log_callback()
	C.avdevice_register_all()
}

func probeAlsaDeviceForBestFormat(deviceName string, channels, sampleRate int) (C.enum_AVSampleFormat, error) {
	formatsToTest := []C.enum_AVSampleFormat{
		C.AV_SAMPLE_FMT_FLT,
		C.AV_SAMPLE_FMT_S32,
		C.AV_SAMPLE_FMT_S16,
	}

	log.Printf("Probing ALSA device '%s' for best sample format...", deviceName)

	var pcmHandle *C.snd_pcm_t
	var hwParams *C.snd_pcm_hw_params_t

	cDeviceName := C.CString(deviceName)
	defer C.free(unsafe.Pointer(cDeviceName))

	if C.snd_pcm_open(&pcmHandle, cDeviceName, C.SND_PCM_STREAM_PLAYBACK, 0) < 0 {
		// CHANGE HERE: Use AV_SAMPLE_FMT_NONE
		return C.AV_SAMPLE_FMT_NONE, fmt.Errorf("cannot open ALSA device %s", deviceName)
	}
	defer C.snd_pcm_close(pcmHandle)

	C.snd_pcm_hw_params_malloc(&hwParams)
	defer C.snd_pcm_hw_params_free(hwParams)
	C.snd_pcm_hw_params_any(pcmHandle, hwParams)

	C.snd_pcm_hw_params_set_access(pcmHandle, hwParams, C.SND_PCM_ACCESS_RW_INTERLEAVED)
	C.snd_pcm_hw_params_set_channels(pcmHandle, hwParams, C.uint(channels))
	rate := C.uint(sampleRate)
	dir := C.int(0)
	C.snd_pcm_hw_params_set_rate_near(pcmHandle, hwParams, &rate, &dir)

	for _, avFormat := range formatsToTest {
		alsaFormat := C.av_to_alsa_format(avFormat)
		if alsaFormat == C.SND_PCM_FORMAT_UNKNOWN {
			continue
		}

		if C.snd_pcm_hw_params_test_format(pcmHandle, hwParams, alsaFormat) == 0 {
			formatName := C.GoString(C.av_get_sample_fmt_name(avFormat))
			log.Printf("Device supports '%s'. Selecting as target format.", formatName)
			return avFormat, nil
		}
	}

	// CHANGE HERE: Use AV_SAMPLE_FMT_NONE
	return C.AV_SAMPLE_FMT_NONE, fmt.Errorf("could not find a supported sample format (S16, S32, FLT) for device %s", deviceName)
}
