#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdio.h>
#include <errno.h>
#include <math.h>

#include "protocol.h" //

#include "libavutil/avstring.h"
#include "libavutil/imgutils.h" // For av_image_get_buffer_size and av_get_bits_per_pixel
#include "libavutil/pixdesc.h"  // For av_pix_fmt_desc_get
#include "libavutil/opt.h"      // For AVOption, OFFSET, AV_OPT_TYPE_*, AV_OPT_FLAG_ENCODING_PARAM (E)
#include "libavformat/avformat.h"
#include "libavformat/internal.h" // For FFOutputFormat
#include "libavutil/intreadwrite.h"
#include "libavcodec/avcodec.h"
#include "libavprivate/libavformat/mux.h" // For FFOutputFormat definition if not provided by avformat.h

// Context for the SHM muxer
typedef struct {
    uint8_t *buffer;      // Pointer to the shared memory buffer
    int buffer_size;
    int shm_fd;
    char shm_name[512];   // Name of the shared memory segment
} SHMMuxerContext;

// write_header: Called when FFmpeg starts writing the output
static int shm_write_header(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    AVStream *st;
    SHMHeader header = {0};
    int ret = 0;

    // Ensure AVIOContext is available for writing command frames
    if (!s->pb) {
        av_log(s, AV_LOG_ERROR, "AVIOContext (s->pb) is NULL. Cannot write command frames.\n");
        return AVERROR(EIO);
    }

    // Retrieve shared memory name from options
    char *shm_name_val = NULL;
    av_opt_get(c, "shm_name", AV_OPT_SEARCH_CHILDREN, (uint8_t **)&shm_name_val);

    if (!shm_name_val) {
        av_log(s, AV_LOG_ERROR, "Missing 'shm_name' option for shm_muxer.\n");
        ret = AVERROR(EINVAL);
        goto fail;
    }

    av_strlcpy(c->shm_name, shm_name_val, sizeof(c->shm_name));
    av_free(shm_name_val); // Free the allocated string by av_opt_get


    if (s->nb_streams == 0) {
        av_log(s, AV_LOG_ERROR, "No streams defined for SHM muxer.\n");
        ret = AVERROR(EINVAL);
        goto fail;
    }

    // Currently assumes only one stream (audio or video) per SHM instance
    st = s->streams[0];


    // Populate SHMHeader based on stream type and parameters
    if (st->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
        av_log(s, AV_LOG_INFO, "shm muxer: Configuring for audio stream.\n");
        header.frametype = 1; // Audio stream
        header.sample_rate = st->codecpar->sample_rate;
        header.channels = st->codecpar->ch_layout.nb_channels;
        header.pix_fmt = st->codecpar->format; // For raw audio, this is sample format

        // Determine bit_depth based on sample format
        switch (st->codecpar->format) {
            case AV_SAMPLE_FMT_FLT:
            case AV_SAMPLE_FMT_FLTP:
                header.bit_depth = 32;
                break;
            case AV_SAMPLE_FMT_S16:
            case AV_SAMPLE_FMT_S16P:
                header.bit_depth = 16;
                break;
            case AV_SAMPLE_FMT_U8:
            case AV_SAMPLE_FMT_U8P:
                header.bit_depth = 8;
                break;
            // Add more formats as needed
            default:
                av_log(s, AV_LOG_ERROR, "Unsupported audio sample format for SHM: %s\n", av_get_sample_fmt_name(st->codecpar->format));
                ret = AVERROR(EINVAL);
                goto fail;
        }

        header.width = 0; // Not directly applicable, can be used for samples per frame
        header.height = 0; // Not directly applicable, can be used for channels
        header.frame_rate = 0; // Not applicable for audio

    } else if (st->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
        av_log(s, AV_LOG_INFO, "shm muxer: Configuring for video stream.\n");
        header.frametype = 0; // Video stream
        header.width = st->codecpar->width;
        header.height = st->codecpar->height;
        header.pix_fmt = st->codecpar->format;
        header.frame_rate = (st->avg_frame_rate.den > 0) ? st->avg_frame_rate.num / st->avg_frame_rate.den : 0;
        header.channels = 0; // Not applicable for video
        header.sample_rate = 0; // Not applicable for video

        // Correct way to get bit depth for video pixel format
        const AVPixFmtDescriptor *desc = av_pix_fmt_desc_get(st->codecpar->format);
        if (desc && (desc->nb_components > 0)) {
            header.bit_depth = desc->comp[0].depth; // Get depth of first component
        } else {
             av_log(s, AV_LOG_WARNING, "Could not determine bit depth for video format %s, defaulting to 8.\n", av_get_pix_fmt_name(st->codecpar->format));
             header.bit_depth = 8; // Default to 8-bit if unable to determine
        }

    } else {
        av_log(s, AV_LOG_ERROR, "shm muxer: Unsupported stream type: %s\n", av_get_media_type_string(st->codecpar->codec_type));
        ret = AVERROR(EINVAL);
        goto fail;
    }

    av_strlcpy(header.shm_file, c->shm_name, sizeof(header.shm_file));
    header.version = 1; // Protocol version

    // Write SHMHeader to s->pb (AVIOContext)
    avio_write(s->pb, (uint8_t*)&header, sizeof(header));
    avio_flush(s->pb); // Ensure header is sent immediately
    // No direct error return from avio_write, assume success if avio_flush doesn't fail

    // 2. Create/Open the shared memory segment
    size_t required_shm_size = 0;
    if (header.frametype == 0) { // Video
        required_shm_size = av_image_get_buffer_size(header.pix_fmt, header.width, header.height, 1);
    } else { // Audio
        required_shm_size = (size_t)header.sample_rate * header.channels * (header.bit_depth / 8) * 2; // 2 seconds buffer
        if (required_shm_size == 0 && st->codecpar->frame_size > 0) {
            required_shm_size = (size_t)st->codecpar->frame_size * st->codecpar->ch_layout.nb_channels * (header.bit_depth / 8);
        }
        if (required_shm_size < 4096) required_shm_size = 4096; // Minimum page size
    }

    c->shm_fd = shm_open(c->shm_name, O_RDWR | O_CREAT, 0666);
    if (c->shm_fd < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to create shared memory '%s': %s\n", c->shm_name, strerror(errno));
        ret = AVERROR(errno);
        goto fail;
    }

    if (ftruncate(c->shm_fd, required_shm_size) != 0) {
        av_log(s, AV_LOG_ERROR, "Failed to ftruncate shared memory '%s': %s\n", c->shm_name, strerror(errno));
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        ret = AVERROR(errno);
        goto fail;
    }
    c->buffer_size = required_shm_size;

    c->buffer = mmap(NULL, c->buffer_size, PROT_READ | PROT_WRITE, MAP_SHARED, c->shm_fd, 0);
    if (c->buffer == MAP_FAILED) {
        av_log(s, AV_LOG_ERROR, "Failed to mmap shared memory '%s': %s\n", c->shm_name, strerror(errno));
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        ret = AVERROR(errno);
        goto fail;
    }

    av_log(s, AV_LOG_INFO, "SHM muxer header written successfully. Shared memory '%s' created (size %d).\n", c->shm_name, c->buffer_size);

    return 0;

fail:
    return ret;
}

// write_packet: Called for each packet (audio frame) to be written
static int shm_write_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMMuxerContext *c = s->priv_data;
    FrameHeader frame_header = {0};

    if (!c->buffer || !s->pb) {
        av_log(s, AV_LOG_ERROR, "SHM muxer context or AVIOContext not properly initialized.\n");
        return AVERROR(EIO);
    }

    // Ensure packet data fits within the shared memory buffer
    if (pkt->size > c->buffer_size) {
        av_log(s, AV_LOG_ERROR, "Packet size (%d) exceeds shared memory buffer size (%d). Dropping packet.\n", pkt->size, c->buffer_size);
        return pkt->size; // Indicate packet was "handled" even if dropped.
    }

    // Copy packet data to shared memory
    memcpy(c->buffer, pkt->data, pkt->size);

    // Populate FrameHeader
    frame_header.cmdtype = 0; // Data frame
    frame_header.size = pkt->size;
    frame_header.pts = pkt->pts; // Use packet PTS directly

    // Write FrameHeader to s->pb (AVIOContext)
    avio_write(s->pb, (uint8_t*)&frame_header, sizeof(frame_header));
    avio_flush(s->pb); // Ensure header is sent immediately
    // No direct error return from avio_write, assume success if avio_flush doesn't fail

    return pkt->size;
}

// write_trailer: Called when FFmpeg finishes writing
static int shm_write_trailer(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    FrameHeader eof_header = {0};

    if (s->pb) {
        // Send EOF command
        eof_header.cmdtype = 2; // EOF command
        avio_write(s->pb, (uint8_t*)&eof_header, sizeof(eof_header));
        avio_flush(s->pb); // Ensure EOF is sent immediately
    }

    // Unmap and close shared memory
    if (c->buffer) {
        munmap(c->buffer, c->buffer_size);
        c->buffer = NULL;
    }
    if (c->shm_fd >= 0) {
        close(c->shm_fd);
        c->shm_fd = -1;
    }

    // Unlink shared memory
    shm_unlink(c->shm_name);

    av_log(s, AV_LOG_INFO, "SHM muxer closed. Shared memory '%s' unlinked.\n", c->shm_name);
    return 0;
}

// Options for the SHM muxer
#define OFFSET(x) offsetof(SHMMuxerContext, x)
#define E AV_OPT_FLAG_ENCODING_PARAM
static const AVOption shm_muxer_options[] = {
    {"shm_name", "Name of the shared memory segment", OFFSET(shm_name), AV_OPT_TYPE_STRING, {.str = NULL}, 0, 0, E},
    {NULL},
};

// Define the private class for options
static const AVClass shm_muxer_class = {
    .class_name = "shm_muxer",
    .item_name  = av_default_item_name,
    .option     = shm_muxer_options,
    .version    = LIBAVUTIL_VERSION_INT,
};

// Define the SHM output format (muxer) explicitly as FFOutputFormat
const FFOutputFormat ff_shm_muxer = {
    .p = { // Public AVOutputFormat fields
        .name           = "shm_muxer",
        .long_name      = "Shared Memory Muxer",
        .mime_type      = "application/octet-stream", // Generic binary stream
        .extensions     = "shm", // A dummy extension, not file-based
        .priv_class     = &shm_muxer_class, // Pointer to the private class for options
    },
    .priv_data_size = sizeof(SHMMuxerContext), // Private data size for the muxer context
    .write_header   = shm_write_header,
    .write_packet   = shm_write_packet,
    .write_trailer  = shm_write_trailer,
};