package encoder

/*
#cgo CFLAGS: -I${SRCDIR}/../../release/include -I${SRCDIR}/../../release/include/arcana
#cgo pkg-config: libavformat libavcodec libavutil libswscale
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>
#include <stdlib.h>

// av_err2str is a macro, so we need a wrapper function
static inline const char* av_error_str(int errnum) {
    static char str[AV_ERROR_MAX_STRING_SIZE];
    av_make_error_string(str, AV_ERROR_MAX_STRING_SIZE, errnum);
    return str;
}

// AVERROR is a macro, so we need a wrapper function
static int averror(int errnum) {
    return AVERROR(errnum);
}
*/
import "C"
import (
	"fmt"
	"log"
	"runtime"
	"unsafe"

	"github.com/richinsley/goshadertoy/options"
)

// Frame represents a single rendered video frame's data, ready for encoding.
type Frame struct {
	Pixels []byte
	PTS    int64
}

// FFmpegEncoder handles the in-process video and audio encoding using FFmpeg libraries.
type FFmpegEncoder struct {
	formatCtx            *C.AVFormatContext
	videoCodecCtx        *C.AVCodecContext
	audioCodecCtx        *C.AVCodecContext
	videoStream          *C.AVStream
	audioStream          *C.AVStream
	swsCtx               *C.struct_SwsContext
	videoFrame           *C.AVFrame
	audioFrame           *C.AVFrame
	videoFrameBuffer     unsafe.Pointer // Reusable buffer for video frames
	videoFrameBufferSize int            // Size of the reusable buffer

	opts        *options.ShaderOptions
	videoFrames chan *Frame
	audioFrames chan []float32
	done        chan error
}

// findBestVideoEncoder attempts to find a suitable video encoder by checking a prioritized list.
// It prefers hardware encoders specific to the platform and falls back to software encoders.
func findBestVideoEncoder(codecPref string) (*C.AVCodec, string) {
	var encoderNames []string

	switch codecPref {
	case "hevc":
		switch runtime.GOOS {
		case "linux":
			encoderNames = []string{"hevc_nvenc", "libx265"}
		case "darwin":
			encoderNames = []string{"hevc_videotoolbox", "libx265"}
		case "windows":
			// Prioritize NVIDIA, then AMD, then Intel, then software
			encoderNames = []string{"hevc_nvenc", "hevc_amf", "hevc_qsv", "libx265"}
		default:
			encoderNames = []string{"libx265"}
		}
	default: // Default to h264
		switch runtime.GOOS {
		case "linux":
			encoderNames = []string{"h264_nvenc", "libx264"}
		case "darwin":
			encoderNames = []string{"h264_videotoolbox", "libx264"}
		case "windows":
			encoderNames = []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"}
		default:
			encoderNames = []string{"libx264"}
		}
	}

	for _, name := range encoderNames {
		cName := C.CString(name)
		codec := C.avcodec_find_encoder_by_name(cName)
		C.free(unsafe.Pointer(cName))
		if codec != nil {
			log.Printf("Selected video encoder: %s", name)
			return codec, name
		}
	}

	return nil, ""
}

func getFFmpegPixFmt(bitDepth int) C.enum_AVPixelFormat {
	switch bitDepth {
	case 10, 12:
		return C.AV_PIX_FMT_P010LE
	default:
		return C.AV_PIX_FMT_NV12
	}
}

func NewFFmpegEncoder(opts *options.ShaderOptions) (*FFmpegEncoder, error) {
	e := &FFmpegEncoder{
		opts:        opts,
		videoFrames: make(chan *Frame, 5),
		// audioFrames: make(chan []float32, 16),
		done:        make(chan error, 1),
	}

	cFilename := C.CString(*opts.OutputFile)
	defer C.free(unsafe.Pointer(cFilename))

	// 1. Allocate format context
	if C.avformat_alloc_output_context2(&e.formatCtx, nil, nil, cFilename) < 0 {
		return nil, fmt.Errorf("could not allocate output context")
	}

	// 2. Find and add video stream
	videoCodec, videoCodecName := findBestVideoEncoder(*opts.Codec)
	if videoCodec == nil {
		return nil, fmt.Errorf("could not find a suitable video encoder for '%s'", *opts.Codec)
	}
	if err := e.addStream(&e.videoStream, &e.videoCodecCtx, videoCodec); err != nil {
		return nil, fmt.Errorf("failed to add video stream: %w", err)
	}

	// 3. Find and add audio stream (if applicable)
	var audioCodec *C.AVCodec
	hasAudio := *opts.AudioInputFile != "" || *opts.AudioInputDevice != ""
	if hasAudio {
		cAACName := C.CString("aac")
		audioCodec = C.avcodec_find_encoder_by_name(cAACName)
		C.free(unsafe.Pointer(cAACName))
		if audioCodec == nil {
			return nil, fmt.Errorf("could not find 'aac' audio encoder")
		}
		if err := e.addStream(&e.audioStream, &e.audioCodecCtx, audioCodec); err != nil {
			return nil, fmt.Errorf("failed to add audio stream: %w", err)
		}
		e.audioFrames = make(chan []float32, 16)
	} else {
		// No audio stream needed, set to nil
		e.audioStream = nil
		e.audioCodecCtx = nil
	}

	// 4. Open codecs
	if err := e.openVideo(videoCodec, videoCodecName, opts); err != nil {
		return nil, err
	}

	// --- Allocate the reusable C buffer for video frames ---
	width := int(*opts.Width)
	height := int(*opts.Height)
	bytesPerPixel := 1
	if *opts.BitDepth > 8 {
		bytesPerPixel = 2
	}
	// The input format is YUV planar, so we need space for 3 planes.
	e.videoFrameBufferSize = width * height * bytesPerPixel * 3
	e.videoFrameBuffer = C.malloc(C.size_t(e.videoFrameBufferSize))
	if e.videoFrameBuffer == nil {
		e.cleanup() // Ensure other resources are freed on failure
		return nil, fmt.Errorf("could not allocate reusable video frame buffer")
	}

	if hasAudio {
		if err := e.openAudio(audioCodec, opts); err != nil {
			return nil, err
		}
	}

	// 5. Open output file and write header
	if (e.formatCtx.oformat.flags & C.AVFMT_NOFILE) == 0 {
		if C.avio_open(&e.formatCtx.pb, cFilename, C.AVIO_FLAG_WRITE) < 0 {
			return nil, fmt.Errorf("could not open output file: %s", *opts.OutputFile)
		}
	}

	if C.avformat_write_header(e.formatCtx, nil) < 0 {
		return nil, fmt.Errorf("could not write header")
	}

	return e, nil
}

func (e *FFmpegEncoder) addStream(st **C.AVStream, codecCtx **C.AVCodecContext, codec *C.AVCodec) error {
	if codec == nil {
		return fmt.Errorf("cannot add stream: provided codec is nil")
	}

	*st = C.avformat_new_stream(e.formatCtx, nil)
	if *st == nil {
		return fmt.Errorf("could not create new stream")
	}
	(*st).id = C.int(e.formatCtx.nb_streams - 1)

	*codecCtx = C.avcodec_alloc_context3(codec)
	if *codecCtx == nil {
		return fmt.Errorf("could not allocate codec context")
	}
	return nil
}

func (e *FFmpegEncoder) openVideo(codec *C.AVCodec, codecName string, opts *options.ShaderOptions) error {
	ctx := e.videoCodecCtx
	ctx.width = C.int(*opts.Width)
	ctx.height = C.int(*opts.Height)
	ctx.time_base = C.AVRational{num: 1, den: C.int(*opts.FPS)}
	ctx.framerate = C.AVRational{num: C.int(*opts.FPS), den: 1}
	ctx.gop_size = 12
	ctx.pix_fmt = getFFmpegPixFmt(*opts.BitDepth)

	// Disable B-frames to prevent frame reordering, which simplifies timestamp handling
	// for real-time encoding.
	ctx.max_b_frames = 0

	// Set encoder-specific options
	switch codecName {
	case "libx264":
		C.av_opt_set(ctx.priv_data, C.CString("preset"), C.CString("slow"), 0)
		// zerolatency tune is crucial for libx264 to avoid reordering and internal buffering.
		C.av_opt_set(ctx.priv_data, C.CString("tune"), C.CString("zerolatency"), 0)
	case "libx265":
		C.av_opt_set(ctx.priv_data, C.CString("preset"), C.CString("slow"), 0)
	case "h264_nvenc", "hevc_nvenc":
		C.av_opt_set(ctx.priv_data, C.CString("preset"), C.CString("p2"), 0)
	}

	if (e.formatCtx.oformat.flags & C.AVFMT_GLOBALHEADER) != 0 {
		ctx.flags |= C.AV_CODEC_FLAG_GLOBAL_HEADER
	}

	if C.avcodec_open2(ctx, codec, nil) < 0 {
		return fmt.Errorf("could not open video codec")
	}

	if C.avcodec_parameters_from_context(e.videoStream.codecpar, ctx) < 0 {
		return fmt.Errorf("could not copy video codec parameters to stream")
	}

	// Initialize the video frame and SWS context for pixel format conversion
	e.videoFrame = C.av_frame_alloc()
	e.videoFrame.format = C.int(ctx.pix_fmt)
	e.videoFrame.width = ctx.width
	e.videoFrame.height = ctx.height
	if C.av_frame_get_buffer(e.videoFrame, 0) < 0 {
		return fmt.Errorf("could not allocate video frame data")
	}

	// The input format from the renderer is YUV Planar (3 separate planes)
	inPixFmt := C.AV_PIX_FMT_YUV444P
	if *opts.BitDepth > 8 {
		inPixFmt = C.AV_PIX_FMT_YUV444P10LE
	}

	e.swsCtx = C.sws_getContext(ctx.width, ctx.height, int32(inPixFmt),
		ctx.width, ctx.height, ctx.pix_fmt,
		C.SWS_BILINEAR, nil, nil, nil)
	if e.swsCtx == nil {
		return fmt.Errorf("could not initialize the conversion context")
	}
	return nil
}

func (e *FFmpegEncoder) openAudio(codec *C.AVCodec, opts *options.ShaderOptions) error {
	ctx := e.audioCodecCtx
	ctx.sample_fmt = C.AV_SAMPLE_FMT_FLTP // Planar float for AAC
	ctx.bit_rate = 192000
	ctx.sample_rate = 44100
	C.av_channel_layout_from_string(&ctx.ch_layout, C.CString("stereo"))

	if (e.formatCtx.oformat.flags & C.AVFMT_GLOBALHEADER) != 0 {
		ctx.flags |= C.AV_CODEC_FLAG_GLOBAL_HEADER
	}

	if C.avcodec_open2(ctx, codec, nil) < 0 {
		return fmt.Errorf("could not open audio codec")
	}

	if C.avcodec_parameters_from_context(e.audioStream.codecpar, ctx) < 0 {
		return fmt.Errorf("could not copy audio codec parameters to stream")
	}

	// Initialize the audio frame
	e.audioFrame = C.av_frame_alloc()
	e.audioFrame.nb_samples = ctx.frame_size
	e.audioFrame.format = C.int(ctx.sample_fmt)
	C.av_channel_layout_copy(&e.audioFrame.ch_layout, &ctx.ch_layout)
	if C.av_frame_get_buffer(e.audioFrame, 0) < 0 {
		return fmt.Errorf("could not allocate audio frame data")
	}

	return nil
}

func (e *FFmpegEncoder) Run() {
	var audioPTS int64 = 0
	internalAudioBuffer := make([]float32, 0, 4096)

	for {
		select {
		case frame, ok := <-e.videoFrames:
			if !ok {
				e.videoFrames = nil // Stop selecting on this channel
			} else {
				e.encodeVideo(frame)
			}
		case audioData, ok := <-e.audioFrames:
			if !ok {
				e.audioFrames = nil // Stop selecting on this channel
			} else {
				internalAudioBuffer = append(internalAudioBuffer, audioData...)
				for len(internalAudioBuffer) >= int(e.audioCodecCtx.frame_size)*2 {
					e.encodeAudio(internalAudioBuffer[:e.audioCodecCtx.frame_size*2], audioPTS)
					internalAudioBuffer = internalAudioBuffer[e.audioCodecCtx.frame_size*2:]
					audioPTS += int64(e.audioCodecCtx.frame_size)
				}
			}
		}

		if e.videoFrames == nil && e.audioFrames == nil {
			break
		}
	}

	// Flush encoders
	e.encode(e.videoStream, e.videoCodecCtx, nil)
	if e.audioStream != nil {
		e.encode(e.audioStream, e.audioCodecCtx, nil)
	}

	// Write trailer and cleanup
	C.av_write_trailer(e.formatCtx)
	e.cleanup()
	e.done <- nil
}

func (e *FFmpegEncoder) encodeVideo(frameData *Frame) {
	if C.av_frame_make_writable(e.videoFrame) < 0 {
		log.Println("Video frame not writable")
		return
	}

	width := int(e.videoFrame.width)
	height := int(e.videoFrame.height)
	bytesPerPixel := 1
	if *e.opts.BitDepth > 8 {
		bytesPerPixel = 2
	}
	planeSize := width * height * bytesPerPixel

	// 1. Copy Go pixel data into our pre-allocated C buffer.
	// This is much faster than allocating new C memory on every frame.
	C.memcpy(e.videoFrameBuffer, unsafe.Pointer(&frameData.Pixels[0]), C.size_t(len(frameData.Pixels)))

	srcPlanes := (**C.uchar)(C.malloc(C.size_t(unsafe.Sizeof((*C.uchar)(nil)) * 4)))
	defer C.free(unsafe.Pointer(srcPlanes))

	srcPlanesSlice := (*[4]*C.uchar)(unsafe.Pointer(srcPlanes))

	// 2. Point the plane pointers to the appropriate locations in our stable C buffer.
	srcPlanesSlice[0] = (*C.uchar)(e.videoFrameBuffer)
	srcPlanesSlice[1] = (*C.uchar)(unsafe.Add(e.videoFrameBuffer, planeSize))
	srcPlanesSlice[2] = (*C.uchar)(unsafe.Add(e.videoFrameBuffer, planeSize*2))
	srcPlanesSlice[3] = nil

	srcStrides := [4]C.int{
		C.int(width * bytesPerPixel),
		C.int(width * bytesPerPixel),
		C.int(width * bytesPerPixel),
		0,
	}

	C.sws_scale(e.swsCtx, srcPlanes, &srcStrides[0], 0, C.int(height),
		&e.videoFrame.data[0], &e.videoFrame.linesize[0])

	e.videoFrame.pts = C.int64_t(frameData.PTS)
	e.encode(e.videoStream, e.videoCodecCtx, e.videoFrame)
}

func (e *FFmpegEncoder) encodeAudio(samples []float32, pts int64) {
	if C.av_frame_make_writable(e.audioFrame) < 0 {
		log.Println("Audio frame not writable")
		return
	}

	// Deinterleave stereo float32 into two planar float32 buffers
	left := (*float32)(unsafe.Pointer(e.audioFrame.data[0]))
	right := (*float32)(unsafe.Pointer(e.audioFrame.data[1]))

	for i := 0; i < int(e.audioFrame.nb_samples); i++ {
		*(*float32)(unsafe.Pointer(uintptr(unsafe.Pointer(left)) + uintptr(i*4))) = samples[i*2]
		*(*float32)(unsafe.Pointer(uintptr(unsafe.Pointer(right)) + uintptr(i*4))) = samples[i*2+1]
	}

	e.audioFrame.pts = C.int64_t(pts)
	e.encode(e.audioStream, e.audioCodecCtx, e.audioFrame)
}

func (e *FFmpegEncoder) encode(st *C.AVStream, ctx *C.AVCodecContext, frame *C.AVFrame) {
	pkt := C.av_packet_alloc()
	defer C.av_packet_free(&pkt)

	// Send the frame to the encoder.
	// If frame is nil, this is a flush signal.
	if C.avcodec_send_frame(ctx, frame) < 0 {
		log.Println("Error sending frame to encoder")
		return
	}

	// Loop to receive all available output packets.
	for {
		ret := C.avcodec_receive_packet(ctx, pkt)
		if ret == C.averror(C.EAGAIN) {
			// The encoder needs more input to produce output. In flush mode (frame is nil),
			// this simply means we need to call receive_packet again.
			// In normal mode, we would break and send the next frame.
			// Since this function handles both cases, we just break here.
			// The outer Run() loop will handle the next step.
			break
		} else if ret == C.AVERROR_EOF {
			// The encoder has been fully flushed.
			break
		} else if ret < 0 {
			log.Printf("Error during encoding: %s", C.GoString(C.av_error_str(ret)))
			break // Stop on a real error.
		}

		// A packet was successfully received, so write it to the output file.
		C.av_packet_rescale_ts(pkt, ctx.time_base, st.time_base)
		pkt.stream_index = st.index

		if C.av_interleaved_write_frame(e.formatCtx, pkt) < 0 {
			log.Println("Error writing packet")
		}
		C.av_packet_unref(pkt)

		// After flushing with a nil frame, we must continue calling
		// receive_packet until it returns AVERROR_EOF.
		if frame == nil {
			continue
		}
	}
}

func (e *FFmpegEncoder) SendVideo(frame *Frame) {
	e.videoFrames <- frame
}

func (e *FFmpegEncoder) SendAudio(samples []float32) {
	if e.audioStream != nil {
		e.audioFrames <- samples
	}
}

func (e *FFmpegEncoder) Close() error {
	close(e.videoFrames)
	if e.audioStream != nil {
		close(e.audioFrames)
	}
	return <-e.done
}

func (e *FFmpegEncoder) cleanup() {
	if e.videoFrameBuffer != nil {
		C.free(e.videoFrameBuffer)
	}
	if e.videoFrame != nil {
		C.av_frame_free(&e.videoFrame)
	}
	if e.videoFrame != nil {
		C.av_frame_free(&e.videoFrame)
	}
	if e.audioFrame != nil {
		C.av_frame_free(&e.audioFrame)
	}
	if e.videoCodecCtx != nil {
		C.avcodec_free_context(&e.videoCodecCtx)
	}
	if e.audioCodecCtx != nil {
		C.avcodec_free_context(&e.audioCodecCtx)
	}
	if e.swsCtx != nil {
		C.sws_freeContext(e.swsCtx)
	}
	if e.formatCtx != nil {
		if (e.formatCtx.oformat.flags & C.AVFMT_NOFILE) == 0 {
			C.avio_closep(&e.formatCtx.pb)
		}
		C.avformat_free_context(e.formatCtx)
	}
}
