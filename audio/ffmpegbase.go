package audio

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"
)

/*
#cgo CFLAGS: -I${SRCDIR}/../../release/include -I${SRCDIR}/../../release/include/arcana
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/channel_layout.h>
#include <libavutil/mathematics.h>
#include <libswresample/swresample.h>


// av_err2str is a macro, so we need a wrapper function
static inline const char* av_error_str(int errnum) {
    static char str[AV_ERROR_MAX_STRING_SIZE];
    return av_make_error_string(str, AV_ERROR_MAX_STRING_SIZE, errnum);
}

// C wrapper to get a channel layout from a string descriptor
static int get_ch_layout_from_string(AVChannelLayout* layout, const char* str) {
    return av_channel_layout_from_string(layout, str);
}
*/
import "C"

// ffmpegBaseDevice contains the common logic for all FFmpeg-based audio devices.
type ffmpegBaseDevice struct {
	audioBaseDevice // Embed the base device
	formatCtx       *C.AVFormatContext
	codecCtx        *C.AVCodecContext
	swrCtx          *C.SwrContext
	audioStream     *C.AVStream
	outChLayout     C.AVChannelLayout
	isStreaming     bool
	decodeLock      sync.Mutex // To protect decoding resources in passive mode
}

// init initializes the FFmpeg libraries and sets up the decoding pipeline.
func (d *ffmpegBaseDevice) init(input, format, channelLayout string, enableRateEmulation bool, inputOptions map[string]string) error {
	d.mode = *d.options.Mode
	d.enableRateEmulation = enableRateEmulation

	// Open Input
	cInput := C.CString(input)
	defer C.free(unsafe.Pointer(cInput))

	var cFormat *C.AVInputFormat
	if format != "" {
		cFormatName := C.CString(format)
		defer C.free(unsafe.Pointer(cFormatName))
		cFormat = C.av_find_input_format(cFormatName)
	}

	var avDict *C.AVDictionary
	for key, value := range inputOptions {
		cKey := C.CString(key)
		cValue := C.CString(value)
		C.av_dict_set(&avDict, cKey, cValue, 0)
		C.free(unsafe.Pointer(cKey))
		C.free(unsafe.Pointer(cValue))
	}
	defer C.av_dict_free(&avDict)

	// avformat_open_input allocates formatCtx, so it must be cleaned up on failure
	if C.avformat_open_input(&d.formatCtx, cInput, cFormat, &avDict) != 0 {
		return fmt.Errorf("failed to open input: %s", input)
	}

	if C.avformat_find_stream_info(d.formatCtx, nil) < 0 {
		d.cleanup() // Cleanup on failure
		return fmt.Errorf("failed to find stream info")
	}

	// Find Audio Stream and Decoder
	streamIndex := -1
	for i := 0; i < int(d.formatCtx.nb_streams); i++ {
		stream := *(**C.AVStream)(unsafe.Pointer(uintptr(unsafe.Pointer(d.formatCtx.streams)) + uintptr(i)*unsafe.Sizeof(*d.formatCtx.streams)))
		if stream.codecpar.codec_type == C.AVMEDIA_TYPE_AUDIO {
			d.audioStream = stream
			streamIndex = i
			break
		}
	}
	if streamIndex == -1 {
		d.cleanup()
		return fmt.Errorf("could not find audio stream")
	}

	decoder := C.avcodec_find_decoder(d.audioStream.codecpar.codec_id)
	if decoder == nil {
		d.cleanup()
		return fmt.Errorf("unsupported codec")
	}

	d.codecCtx = C.avcodec_alloc_context3(decoder)
	if d.codecCtx == nil {
		d.cleanup()
		return fmt.Errorf("failed to allocate codec context")
	}

	if C.avcodec_parameters_to_context(d.codecCtx, d.audioStream.codecpar) < 0 {
		d.cleanup()
		return fmt.Errorf("failed to copy codec parameters to context")
	}

	if C.avcodec_open2(d.codecCtx, decoder, nil) < 0 {
		d.cleanup()
		return fmt.Errorf("failed to open codec")
	}

	// Setup Resampler
	d.sampleRate = int(d.codecCtx.sample_rate)

	cLayoutStr := C.CString(channelLayout) // Use the passed-in channel layout
	defer C.free(unsafe.Pointer(cLayoutStr))
	if C.get_ch_layout_from_string(&d.outChLayout, cLayoutStr) != 0 {
		d.cleanup()
		return fmt.Errorf("invalid output channel layout: %s", channelLayout)
	}

	ret := C.swr_alloc_set_opts2(&d.swrCtx,
		&d.outChLayout, C.AV_SAMPLE_FMT_FLT, C.int(d.sampleRate),
		&d.codecCtx.ch_layout, d.codecCtx.sample_fmt, d.codecCtx.sample_rate, 0, nil)

	if ret < 0 {
		d.cleanup()
		return fmt.Errorf("failed to allocate resampler context")
	}
	C.swr_init(d.swrCtx)

	return nil
}

// start begins the audio processing loop.
func (d *ffmpegBaseDevice) Start() error {
	var ctx context.Context
	ctx, d.cancel = context.WithCancel(context.Background())

	// Only start the active decoding goroutine for real-time modes.
	if d.mode == "live" || d.mode == "stream" {
		go d.runAudioLoop(ctx)
	}

	if d.player != nil {
		d.player.Start(d.buffer)
	}
	return nil
}

// runAudioLoop is the active decoding loop for live/stream modes.
func (d *ffmpegBaseDevice) runAudioLoop(ctx context.Context) {
	defer d.cleanup()

	packet := C.av_packet_alloc()
	defer C.av_packet_free(&packet)
	frame := C.av_frame_alloc()
	defer C.av_frame_free(&frame)

	d.startTime = time.Now()
	// d.samplesSent is already initialized to 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if C.av_read_frame(d.formatCtx, packet) < 0 {
				return // End of stream or error
			}

			if packet.stream_index == C.int(d.audioStream.index) {
				if C.avcodec_send_packet(d.codecCtx, packet) < 0 {
					C.av_packet_unref(packet)
					continue
				}

				for C.avcodec_receive_frame(d.codecCtx, frame) == 0 {
					d.resampleAndBuffer(frame)
					C.av_frame_unref(frame)
				}
			}
			C.av_packet_unref(packet)

			// Rate emulation for file-based streaming
			if d.enableRateEmulation {
				elapsed := time.Since(d.startTime)
				expectedSamples := int64(elapsed.Seconds() * float64(d.sampleRate))
				if d.samplesSent > expectedSamples {
					aheadSamples := d.samplesSent - expectedSamples
					sleepDuration := time.Duration(float64(aheadSamples)*1e9/float64(d.sampleRate)) * time.Nanosecond
					time.Sleep(sleepDuration)
				}
			}
		}
	}
}

func (d *ffmpegBaseDevice) DecodeUntil(targetSample int64) error {
	d.decodeLock.Lock()
	defer d.decodeLock.Unlock()

	// If we've already decoded past the target, there's nothing to do.
	if d.samplesSent >= targetSample {
		return nil
	}

	packet := C.av_packet_alloc()
	defer C.av_packet_free(&packet)
	frame := C.av_frame_alloc()
	defer C.av_frame_free(&frame)

	// Keep decoding until we reach the target number of samples.
	for d.samplesSent < targetSample {
		if C.av_read_frame(d.formatCtx, packet) < 0 {
			// End of file or error
			return fmt.Errorf("EOF or read error while decoding to sample %d", targetSample)
		}

		if packet.stream_index == C.int(d.audioStream.index) {
			if C.avcodec_send_packet(d.codecCtx, packet) < 0 {
				C.av_packet_unref(packet)
				continue
			}

			for C.avcodec_receive_frame(d.codecCtx, frame) == 0 {
				d.resampleAndBuffer(frame)
				C.av_frame_unref(frame)
				// Break inner loop if we've passed the target, to avoid over-decoding.
				if d.samplesSent >= targetSample {
					break
				}
			}
		}
		C.av_packet_unref(packet)
	}
	return nil
}

func (d *ffmpegBaseDevice) resampleAndBuffer(frame *C.AVFrame) {
	// Get the estimated output sample count from SWR context
	estimatedOutputSamples := C.swr_get_out_samples(d.swrCtx, frame.nb_samples)
	if estimatedOutputSamples < 0 {
		log.Printf("Error: Could not estimate output samples: %d", estimatedOutputSamples)
		return
	}

	// Add a small buffer for safety (SWR might produce slightly more due to filtering)
	maxOutputSamples := estimatedOutputSamples + 32

	resampledFrame := C.av_frame_alloc()
	defer C.av_frame_free(&resampledFrame)

	resampledFrame.sample_rate = C.int(d.sampleRate)
	C.av_channel_layout_copy(&resampledFrame.ch_layout, &d.outChLayout)
	resampledFrame.format = C.AV_SAMPLE_FMT_FLT
	resampledFrame.nb_samples = maxOutputSamples

	if C.av_frame_get_buffer(resampledFrame, 0) < 0 {
		log.Println("Error: Could not allocate buffer for resampled frame")
		return
	}

	// Perform the actual resampling conversion
	actualOutputSamples := C.swr_convert(
		d.swrCtx,
		&resampledFrame.data[0],
		maxOutputSamples,
		&frame.data[0],
		frame.nb_samples,
	)

	if actualOutputSamples < 0 {
		log.Printf("Error: swr_convert failed: %d", actualOutputSamples)
		return
	}

	if actualOutputSamples == 0 {
		// No output samples produced (this can happen with some filters)
		return
	}

	// Use the actual number of samples produced by swr_convert
	numSamples := int(actualOutputSamples)
	numChannels := int(d.outChLayout.nb_channels)

	// Create Go slice from the actual samples produced
	totalFloats := numSamples * numChannels
	goSlice := (*[1 << 30]float32)(unsafe.Pointer(resampledFrame.data[0]))[:totalFloats]
	dataCopy := make([]float32, totalFloats)
	copy(dataCopy, goSlice)

	// Write to buffer and update sample count
	d.buffer.Write(dataCopy, false)
	d.samplesSent += int64(numSamples)
}

// cleanup frees all allocated FFmpeg resources.
func (d *ffmpegBaseDevice) cleanup() {
	log.Println("Cleaning up FFmpeg resources...")
	C.av_channel_layout_uninit(&d.outChLayout)
	if d.swrCtx != nil {
		C.swr_free(&d.swrCtx)
	}
	if d.codecCtx != nil {
		C.avcodec_free_context(&d.codecCtx)
	}
	if d.formatCtx != nil {
		C.avformat_close_input(&d.formatCtx)
	}
}

// Stop signals the audio loop to terminate.
func (d *ffmpegBaseDevice) Stop() error {
	if !d.isStreaming {
		return nil
	}
	d.isStreaming = false
	if d.cancel != nil {
		d.cancel()
	}

	if d.player != nil {
		d.player.Stop()
	}
	return nil
}

// SampleRate returns the detected sample rate of the audio stream.
func (d *ffmpegBaseDevice) SampleRate() int {
	return d.sampleRate
}

// GetBuffer returns the shared audio buffer.
func (d *ffmpegBaseDevice) GetBuffer() *SharedAudioBuffer {
	return d.buffer
}
