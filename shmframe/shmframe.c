#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>

#include "shmframe.h"
#include "protocol.h"

#include "libavutil/avstring.h"
#include "libavutil/imgutils.h"
#include "libavformat/avformat.h"
#include "libavformat/internal.h"
#include "libavutil/intreadwrite.h"
#include "libavcodec/avcodec.h"

typedef struct {
    uint8_t *buffer; // Pointer to the shared memory buffer
    int buffer_size;
    int shm_fd;
    int current_stream_index; // To track which stream (video/audio) to associate packets with if combined
} SHMDemuxerContext;

static int shm_read_header(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    SHMHeader header;
    AVStream *st;

    // This will now correctly block and wait for the client to send data
    if (avio_read(s->pb, (unsigned char*)&header, sizeof(header)) != sizeof(header)) {
        av_log(s, AV_LOG_ERROR, "Failed to read initial SHMHeader from pipe. Client may have disconnected.\n");
        return AVERROR(EIO);
    }

    c->shm_fd = shm_open(header.shm_file, O_RDONLY, 0666);
    if (c->shm_fd < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to open shared memory '%s': %s\n", header.shm_file, strerror(errno));
        return AVERROR(errno);
    }

    struct stat st_shm;
    if (fstat(c->shm_fd, &st_shm) < 0) {
        close(c->shm_fd);
        av_log(s, AV_LOG_ERROR, "fstat on shared memory failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }
    c->buffer_size = st_shm.st_size;

    c->buffer = mmap(NULL, c->buffer_size, PROT_READ, MAP_SHARED, c->shm_fd, 0);
    if (c->buffer == MAP_FAILED) {
        close(c->shm_fd);
        av_log(s, AV_LOG_ERROR, "mmap failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }

    st = avformat_new_stream(s, NULL);
    if (!st) {
        munmap(c->buffer, c->buffer_size);
        close(c->shm_fd);
        return AVERROR(ENOMEM);
    }

    if (header.frametype == 0) { // Video stream
        av_log(s, AV_LOG_INFO, "shm demuxer: Configuring for video stream.\n");
        if (header.frame_rate > 0) {
            st->time_base = av_d2q(1.0 / header.frame_rate, 1000000);
            st->r_frame_rate = av_d2q(header.frame_rate, 1000000);
        } else {
            st->time_base = (AVRational){1, 25};
            st->r_frame_rate = (AVRational){25, 1};
        }
        st->avg_frame_rate = st->r_frame_rate;

        st->codecpar->codec_type = AVMEDIA_TYPE_VIDEO;
        st->codecpar->codec_id = AV_CODEC_ID_RAWVIDEO; // Assuming raw video input
        st->codecpar->width = header.width;
        st->codecpar->height = header.height;
        st->codecpar->format = header.pix_fmt;
        st->codecpar->codec_tag = 0; // Not strictly needed for raw video
        c->current_stream_index = 0; // Set this stream as the primary for packets
    } else if (header.frametype == 1) { // Audio stream
        av_log(s, AV_LOG_INFO, "shm demuxer: Configuring for audio stream.\n");
        st->codecpar->codec_type = AVMEDIA_TYPE_AUDIO;
        // Choose appropriate PCM codec based on bit_depth.
        // Assuming little-endian for simplicity, adjust if needed.
        if (header.bit_depth == 32) {
            st->codecpar->codec_id = AV_CODEC_ID_PCM_F32LE; // 32-bit float PCM, little-endian
            st->codecpar->format = AV_SAMPLE_FMT_FLT; // Corresponds to float
        } else if (header.bit_depth == 16) {
            st->codecpar->codec_id = AV_CODEC_ID_PCM_S16LE; // 16-bit signed int PCM, little-endian
            st->codecpar->format = AV_SAMPLE_FMT_S16; // Corresponds to signed 16-bit
        } else {
            av_log(s, AV_LOG_ERROR, "Unsupported audio bit depth: %d\n", header.bit_depth);
            munmap(c->buffer, c->buffer_size);
            close(c->shm_fd);
            return AVERROR(EINVAL);
        }

        av_channel_layout_default(&st->codecpar->ch_layout, header.channels); // Set layout based on nb_channels from header


        st->codecpar->sample_rate = header.sample_rate;
        st->time_base = (AVRational){1, header.sample_rate}; // Time base for audio streams
        c->current_stream_index = 0; // Assuming this is the first (and possibly only) stream for this pipe
    } else {
        av_log(s, AV_LOG_ERROR, "shm demuxer: Unsupported frame type: %d\n", header.frametype);
        munmap(c->buffer, c->buffer_size);
        close(c->shm_fd);
        return AVERROR(EINVAL);
    }

    av_log(s, AV_LOG_INFO, "shm demuxer header read successfully.\n");

    return 0;
}

static int shm_read_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMDemuxerContext *c = s->priv_data;
    FrameHeader frame_header; //
    int ret;

    ret = avio_read(s->pb, (unsigned char*)&frame_header, sizeof(frame_header));

    if (ret == 0) {
        printf("EOF reached on command pipe.\n");
        av_log(s, AV_LOG_INFO, "Client closed the command pipe. Shutting down.\n");
        return AVERROR_EOF;
    }
    if (ret < 0) {
        printf("Error reading from command pipe: %s\n", av_err2str(ret));
        av_log(s, AV_LOG_ERROR, "Error reading from command pipe: %s\n", av_err2str(ret));
        return ret;
    }
    if (ret != sizeof(frame_header)) {
        printf("Protocol error: Incomplete frame header read. Expected %zu, got %d.\n", sizeof(frame_header), ret);
        av_log(s, AV_LOG_ERROR, "Protocol error: Incomplete frame header read. Expected %zu, got %d.\n", sizeof(frame_header), ret);
        return AVERROR(EIO);
    }

    if (frame_header.cmdtype == 2) { // CMD_TYPE_EOF
        printf("Received explicit EOF command.\n");
        av_log(s, AV_LOG_INFO, "Received explicit EOF command. Shutting down.\n");
        return AVERROR_EOF;
    }

    ret = av_new_packet(pkt, frame_header.size);
    if (ret < 0) { //
        av_log(s, AV_LOG_ERROR, "Failed to allocate new packet.\n");
        return ret;
    }

    if (frame_header.size > c->buffer_size) {
        printf("Frame size (%d) exceeds shared memory buffer size (%d).\n", frame_header.size, c->buffer_size);
        av_log(s, AV_LOG_ERROR, "Frame size (%d) exceeds shared memory buffer size (%d).\n", frame_header.size, c->buffer_size);
        av_packet_unref(pkt);
        return AVERROR(EINVAL);
    }

    memcpy(pkt->data, c->buffer, frame_header.size);
    pkt->pts = frame_header.pts;
    pkt->dts = pkt->pts;
    pkt->stream_index = c->current_stream_index; // Assign to the configured stream
    pkt->flags |= AV_PKT_FLAG_KEY; // This flag might need to be conditional for audio. Raw PCM often doesn't need it.

    return frame_header.size;
}

static int shm_read_close(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    if (c->buffer) {
        munmap(c->buffer, c->buffer_size);
        c->buffer = NULL;
    }
    if (c->shm_fd >= 0) {
        close(c->shm_fd);
        c->shm_fd = -1;
    }
    av_log(s, AV_LOG_INFO, "Shared memory demuxer closed.\n");
    return 0;
}

const FFInputFormat ff_shm_demuxer = {
    .p = {
        .name           = "shm_demuxer",
        .long_name      = "Shared Memory Demuxer",
    },
    .priv_data_size = sizeof(SHMDemuxerContext),
    .read_header    = shm_read_header,
    .read_packet    = shm_read_packet,
    .read_close     = shm_read_close,
};