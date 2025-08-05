//go:build linux && cgo
// +build linux,cgo

package arcana

/*
#cgo CFLAGS: -I${SRCDIR}/../release/include -I${SRCDIR}/../release/include/arcana
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libavformat_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libavcodec_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libavfilter_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libavdevice_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libswresample_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libswscale_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libpostproc_arcana.so
#cgo LDFLAGS: ${SRCDIR}/../release/lib/libavutil_arcana.so

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavfilter/avfilter.h>
#include <libavdevice/avdevice.h>
#include <libpostproc/postprocess.h>
#include <libswresample/swresample.h>
#include <libswscale/swscale.h>
#include <stdio.h>

// A simple C log callback that prints directly to stderr.
static void simple_log_callback(void* ptr, int level, const char* fmt, va_list vl) {
    // To prevent FFmpeg's verbose logs from cluttering the console,
    // we can filter to only show important messages.
    // AV_LOG_INFO is a good balance. For more detail, use AV_LOG_DEBUG.
    if (level > AV_LOG_DEBUG) {
        return;
    }

    // Prepend a tag to identify FFmpeg logs and print to standard error.
    fprintf(stderr, "[FFmpeg] ");
    vfprintf(stderr, fmt, vl);
}

// Function to set the callback
static void set_log_callback() {
    av_log_set_callback(simple_log_callback);
}
*/
import "C"

func Platform_init() {
	// Set the log level. AV_LOG_INFO is a good default.
	// Use AV_LOG_DEBUG for more verbose output when needed.
	C.av_log_set_level(C.AV_LOG_INFO)
	// Set our simple C function as the callback
	C.set_log_callback()

	// Register all available device muxers and demuxers
	C.avdevice_register_all()

	// fmt.Printf("libavcodec version:    %d\n", uint(C.avcodec_version()))
	// fmt.Printf("libavformat version:   %d\n", uint(C.avformat_version()))
	// fmt.Printf("libavutil version:     %d\n", uint(C.avutil_version()))
	// fmt.Printf("libavfilter version:   %d\n", uint(C.avfilter_version()))
	// fmt.Printf("libavdevice version:   %d\n", uint(C.avdevice_version()))
	// fmt.Printf("libpostproc version:   %d\n", uint(C.postproc_version()))
	// fmt.Printf("libswresample version: %d\n", uint(C.swresample_version()))
	// fmt.Printf("libswscale version:    %d\n", uint(C.swscale_version()))
}
