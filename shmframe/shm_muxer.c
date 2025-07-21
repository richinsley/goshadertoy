#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdio.h>
#include <errno.h>
#include <math.h>
#include <semaphore.h>

#include "protocol.h"

#include "libavutil/avstring.h"
#include "libavutil/imgutils.h"
#include "libavutil/mem.h"
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
    const AVClass *class;
    // SHM and Semaphore resources
    uint8_t *buffer;
    int buffer_size;
    int shm_fd;
    char shm_name[512];
    char empty_sem_name[256];
    char full_sem_name[256];
    sem_t *empty_sem;
    sem_t *full_sem;
    SHMControlBlock *control_block;
    uint8_t *frame_buffers[NUM_BUFFERS];
    uint32_t write_index;

    // Muxer options
    int samples_per_buffer;

    // Internal buffering state
    uint8_t *internal_buffer;
    int internal_buffer_size;
    int internal_buffer_occupancy;
    size_t frame_buffer_size;
    int64_t pts_counter;

} SHMMuxerContext;

// Helper function to write one full, buffered frame to shared memory
static int write_full_frame(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;

    if (sem_wait(c->empty_sem) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_wait(empty_sem) failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }

    uint8_t *write_buffer = c->frame_buffers[c->write_index];
    uint64_t offset = write_buffer - c->buffer;

    // Copy one full frame from our internal buffer to the shared memory slot
    memcpy(write_buffer, c->internal_buffer, c->frame_buffer_size);

    FrameHeader frame_header = {0};
    frame_header.cmdtype = 0;
    frame_header.size = c->frame_buffer_size;
    frame_header.pts = c->pts_counter++;
    frame_header.offset = offset;

    avio_write(s->pb, (uint8_t*)&frame_header, sizeof(frame_header));
    avio_flush(s->pb);

    c->write_index = (c->write_index + 1) % NUM_BUFFERS;

    if (sem_post(c->full_sem) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_post(full_sem) failed: %s\n", strerror(errno));
    }

    // Shift the remaining data in the internal buffer to the beginning
    c->internal_buffer_occupancy -= c->frame_buffer_size;
    if (c->internal_buffer_occupancy > 0) {
        memmove(c->internal_buffer, c->internal_buffer + c->frame_buffer_size, c->internal_buffer_occupancy);
    }

    return 0;
}


// write_header: Called when FFmpeg starts writing the output
static int shm_write_header(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    AVStream *st;
    if (s->nb_streams == 0) {
        av_log(s, AV_LOG_ERROR, "No streams were mapped to the SHM muxer.\n");
        return AVERROR(EINVAL);
    }
    st = s->streams[0];
    SHMHeader header = {0};
    int bytes_per_sample;
    size_t required_shm_size;

    c->pts_counter = 0;

    pid_t pid = getpid();
    snprintf(c->shm_name, sizeof(c->shm_name), "/goshadertoy_audio_%d", pid);
    snprintf(c->empty_sem_name, sizeof(c->empty_sem_name), "goshadertoy_audio_empty_%d", pid);
    snprintf(c->full_sem_name, sizeof(c->full_sem_name), "goshadertoy_audio_full_%d", pid);

    if (st->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
        header.frametype = 1;
        header.sample_rate = st->codecpar->sample_rate;
        header.channels = st->codecpar->ch_layout.nb_channels;
        header.pix_fmt = st->codecpar->format; // This is actually sample format
        bytes_per_sample = av_get_bytes_per_sample(st->codecpar->format);
        header.bit_depth = bytes_per_sample * 8;
    } else {
        av_log(s, AV_LOG_ERROR, "SHM muxer only supports audio streams.\n");
        return AVERROR(EINVAL);
    }

    c->frame_buffer_size = c->samples_per_buffer * header.channels * bytes_per_sample;

    // Allocate internal buffer to accumulate data. Make it twice the size of a frame
    // to handle incoming packets larger than one frame.
    c->internal_buffer_size = c->frame_buffer_size * 2;
    c->internal_buffer = av_malloc(c->internal_buffer_size);
    if (!c->internal_buffer) {
        return AVERROR(ENOMEM);
    }
    c->internal_buffer_occupancy = 0;


    av_strlcpy(header.shm_file, c->shm_name, sizeof(header.shm_file));
    av_strlcpy(header.empty_sem_name, c->empty_sem_name, sizeof(header.empty_sem_name));
    av_strlcpy(header.full_sem_name, c->full_sem_name, sizeof(header.full_sem_name));
    header.version = 1;

    avio_write(s->pb, (uint8_t*)&header, sizeof(header));
    avio_flush(s->pb);

    required_shm_size = sizeof(SHMControlBlock) + (c->frame_buffer_size * NUM_BUFFERS);

    c->shm_fd = shm_open(c->shm_name, O_RDWR | O_CREAT, 0666);
    if (c->shm_fd < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to create shared memory '%s': %s\n", c->shm_name, strerror(errno));
        return AVERROR(errno);
    }

    c->empty_sem = sem_open(c->empty_sem_name, O_CREAT, 0666, NUM_BUFFERS);
    if (c->empty_sem == SEM_FAILED) {
        av_log(s, AV_LOG_ERROR, "Failed to create empty semaphore '%s': %s\n", c->empty_sem_name, strerror(errno));
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        return AVERROR(errno);
    }

    c->full_sem = sem_open(c->full_sem_name, O_CREAT, 0666, 0);
    if (c->full_sem == SEM_FAILED) {
        av_log(s, AV_LOG_ERROR, "Failed to create full semaphore '%s': %s\n", c->full_sem_name, strerror(errno));
        close(c->shm_fd);
        shm_unlink(c->shm_name);
        sem_close(c->empty_sem);
        sem_unlink(c->empty_sem_name);
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
    c->control_block->num_buffers = NUM_BUFFERS;
    c->control_block->eof = 0;
    c->write_index = 0;

    for(int i=0; i < NUM_BUFFERS; i++) {
        c->frame_buffers[i] = c->buffer + sizeof(SHMControlBlock) + (i * c->frame_buffer_size);
    }

    av_log(s, AV_LOG_INFO, "SHM muxer header written. SHM '%s' created (size %d), frame buffer size %zu.\n", c->shm_name, c->buffer_size, c->frame_buffer_size);

    return 0;
}

// write_packet: Called for each packet to be written
static int shm_write_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMMuxerContext *c = s->priv_data;

    if (c->internal_buffer_occupancy + pkt->size > c->internal_buffer_size) {
        av_log(s, AV_LOG_ERROR, "Internal muxer buffer overflow! Dropping data.\n");
        return 0;
    }

    memcpy(c->internal_buffer + c->internal_buffer_occupancy, pkt->data, pkt->size);
    c->internal_buffer_occupancy += pkt->size;

    while (c->internal_buffer_occupancy >= c->frame_buffer_size) {
        int ret = write_full_frame(s);
        if (ret < 0) {
            return ret;
        }
    }

    return 0;
}

static int shm_write_trailer(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    
    // Flush any remaining data in the internal buffer by padding it with silence
    if (c->internal_buffer_occupancy > 0) {
        int padding_size = c->frame_buffer_size - c->internal_buffer_occupancy;
        memset(c->internal_buffer + c->internal_buffer_occupancy, 0, padding_size);
        c->internal_buffer_occupancy += padding_size;
        write_full_frame(s);
    }

    c->control_block->eof = 1;

    FrameHeader eof_header = { .cmdtype = 2 };
    avio_write(s->pb, (uint8_t*)&eof_header, sizeof(eof_header));
    avio_flush(s->pb);
    
    // Cleanup
    av_free(c->internal_buffer);
    munmap(c->buffer, c->buffer_size);
    close(c->shm_fd);
    shm_unlink(c->shm_name);

    if (c->empty_sem != SEM_FAILED) {
        sem_close(c->empty_sem);
        sem_unlink(c->empty_sem_name);
    }
    if (c->full_sem != SEM_FAILED) {
        sem_close(c->full_sem);
        sem_unlink(c->full_sem_name);
    }
    return 0;
}

#define OFFSET(x) offsetof(SHMMuxerContext, x)
static const AVOption shm_muxer_options[] = {
    { "samples_per_buffer", "Number of audio samples per shared memory buffer", OFFSET(samples_per_buffer), AV_OPT_TYPE_INT, { .i64 = 1024 }, 256, 16384, AV_OPT_FLAG_ENCODING_PARAM },
    { NULL }
};

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
        .video_codec    = AV_CODEC_ID_NONE,
    },
    .priv_data_size = sizeof(SHMMuxerContext),
    .write_header   = shm_write_header,
    .write_packet   = shm_write_packet,
    .write_trailer  = shm_write_trailer,
};