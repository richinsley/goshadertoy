#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdio.h>
#include <errno.h>
#include <math.h>

#include "protocol.h"

#include "libavutil/avstring.h"
#include "libavutil/imgutils.h"
#include "libavutil/pixdesc.h"
#include "libavutil/opt.h"
#include "libavformat/avformat.h"
#include "libavformat/internal.h"
#include "libavutil/intreadwrite.h"
#include "libavcodec/avcodec.h"
#include "libavprivate/libavformat/mux.h"

#define NUM_BUFFERS 3 // Use a ring buffer with 3 slots

// Context for the SHM muxer
typedef struct {
    uint8_t *buffer;
    int buffer_size;
    int shm_fd;
    char shm_name[512];
    SHMControlBlock *control_block;
    uint8_t *frame_buffers[NUM_BUFFERS];
} SHMMuxerContext;

// write_header: Called when FFmpeg starts writing the output
static int shm_write_header(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    AVStream *st;
    SHMHeader header = {0};

    if (!s->pb) {
        av_log(s, AV_LOG_ERROR, "AVIOContext is NULL.\n");
        return AVERROR(EIO);
    }
    
    if (s->nb_streams == 0) {
        av_log(s, AV_LOG_ERROR, "No streams were mapped to the SHM muxer.\n");
        return AVERROR(EINVAL);
    }
    
    st = s->streams[0];
    
    snprintf(c->shm_name, sizeof(c->shm_name), "/goshadertoy_%d", getpid());

    // Populate SHMHeader based on the stream FFmpeg has provided
    if (st->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
        header.frametype = 1;
        header.sample_rate = st->codecpar->sample_rate;
        header.channels = st->codecpar->ch_layout.nb_channels;
        header.pix_fmt = st->codecpar->format;

        switch (st->codecpar->format) {
            case AV_SAMPLE_FMT_FLT: case AV_SAMPLE_FMT_FLTP: header.bit_depth = 32; break;
            case AV_SAMPLE_FMT_S16: case AV_SAMPLE_FMT_S16P: header.bit_depth = 16; break;
            case AV_SAMPLE_FMT_U8:  case AV_SAMPLE_FMT_U8P:  header.bit_depth = 8;  break;
            default:
                av_log(s, AV_LOG_ERROR, "Unsupported audio sample format for SHM: %s\n", av_get_sample_fmt_name(st->codecpar->format));
                return AVERROR(EINVAL);
        }
    } else if (st->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
        header.frametype = 0;
    } else {
        av_log(s, AV_LOG_ERROR, "Unsupported stream type provided to SHM muxer.\n");
        return AVERROR(EINVAL);
    }

    av_strlcpy(header.shm_file, c->shm_name, sizeof(header.shm_file));
    header.version = 1;

    avio_write(s->pb, (uint8_t*)&header, sizeof(header));
    avio_flush(s->pb);

    size_t frame_size = (size_t)header.sample_rate * header.channels * (header.bit_depth / 8) * 2;
    if (frame_size < 4096) frame_size = 4096;

    size_t required_shm_size = sizeof(SHMControlBlock) + (frame_size * NUM_BUFFERS);

    c->shm_fd = shm_open(c->shm_name, O_RDWR | O_CREAT, 0666);
    if (c->shm_fd < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to create shared memory '%s': %s\n", c->shm_name, strerror(errno));
        return AVERROR(errno);
    }

    if (ftruncate(c->shm_fd, required_shm_size) != 0) {
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        return AVERROR(errno);
    }
    c->buffer_size = required_shm_size;

    c->buffer = mmap(NULL, c->buffer_size, PROT_READ | PROT_WRITE, MAP_SHARED, c->shm_fd, 0);
    if (c->buffer == MAP_FAILED) {
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        return AVERROR(errno);
    }

    c->control_block = (SHMControlBlock*)c->buffer;
    c->control_block->write_index = 0;
    c->control_block->read_index = 0;
    c->control_block->num_buffers = NUM_BUFFERS;
    c->control_block->eof = 0;

    for(int i=0; i < NUM_BUFFERS; i++) {
        c->frame_buffers[i] = c->buffer + sizeof(SHMControlBlock) + (i * frame_size);
    }


    av_log(s, AV_LOG_INFO, "SHM muxer header written. SHM '%s' created (size %d).\n", c->shm_name, c->buffer_size);

    return 0;
}

// write_packet: Called for each packet to be written
static int shm_write_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMMuxerContext *c = s->priv_data;

    // Wait for a free slot in the ring buffer
    while ((c->control_block->write_index + 1) % c->control_block->num_buffers == c->control_block->read_index) {
        usleep(1000); // Sleep for 1ms
    }

    uint8_t *write_buffer = c->frame_buffers[c->control_block->write_index];

    FrameHeader frame_header = {0};
    memcpy(write_buffer, pkt->data, pkt->size);
    frame_header.cmdtype = 0;
    frame_header.size = pkt->size;
    frame_header.pts = pkt->pts;
    avio_write(s->pb, (uint8_t*)&frame_header, sizeof(frame_header));
    avio_flush(s->pb);
    
    // Update the write index
    c->control_block->write_index = (c->control_block->write_index + 1) % c->control_block->num_buffers;

    return pkt->size;
}

static int shm_write_trailer(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    
    c->control_block->eof = 1;

    FrameHeader eof_header = { .cmdtype = 2 };
    avio_write(s->pb, (uint8_t*)&eof_header, sizeof(eof_header));
    avio_flush(s->pb);
    munmap(c->buffer, c->buffer_size);
    close(c->shm_fd);
    shm_unlink(c->shm_name);
    return 0;
}


static const AVOption shm_muxer_options[] = { {NULL} };

static const AVClass shm_muxer_class = {
    .class_name = "shm_muxer",
    .item_name  = av_default_item_name,
    .option     = shm_muxer_options,
    .version    = LIBAVUTIL_VERSION_INT,
};

const FFOutputFormat ff_shm_muxer = {
    .p = {
        .name           = "shm_muxer",
        .long_name      = "Shared Memory Muxer",
        .priv_class     = &shm_muxer_class,
        .audio_codec    = AV_CODEC_ID_PCM_F32LE,
        .video_codec    = AV_CODEC_ID_RAWVIDEO,
    },
    .priv_data_size = sizeof(SHMMuxerContext),
    .write_header   = shm_write_header,
    .write_packet   = shm_write_packet,
    .write_trailer  = shm_write_trailer,
};