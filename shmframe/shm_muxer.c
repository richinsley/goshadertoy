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
    
    // Video resources
    uint8_t *buffer_video;
    int buffer_size_video;
    int shm_fd_video;
    sem_t *empty_sem_video;
    sem_t *full_sem_video;
    SHMControlBlock *control_block_video;
    uint8_t *frame_buffers_video[NUM_BUFFERS];
    uint32_t write_index_video;
    size_t frame_buffer_size_video;

    // Audio resources
    uint8_t *buffer_audio;
    int buffer_size_audio;
    int shm_fd_audio;
    sem_t *empty_sem_audio;
    sem_t *full_sem_audio;
    SHMControlBlock *control_block_audio;
    uint8_t *frame_buffers_audio[NUM_BUFFERS];
    uint32_t write_index_audio;
    size_t frame_buffer_size_audio;

    // Muxer options
    int samples_per_buffer;

    // Internal buffering state for audio
    uint8_t *internal_buffer_audio;
    int internal_buffer_size_audio;
    int internal_buffer_occupancy_audio;
    int64_t pts_counter_audio;

} SHMMuxerContext;

// Helper function to write one full, buffered audio frame to shared memory
static int write_full_audio_frame(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;

    if (sem_wait(c->empty_sem_audio) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_wait(empty_sem_audio) failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }

    uint8_t *write_buffer = c->frame_buffers_audio[c->write_index_audio];
    uint64_t offset = write_buffer - c->buffer_audio;

    // Copy one full frame from our internal buffer to the shared memory slot
    memcpy(write_buffer, c->internal_buffer_audio, c->frame_buffer_size_audio);

    FrameHeader frame_header = {0};
    frame_header.cmdtype = 1;
    frame_header.size = c->frame_buffer_size_audio;
    frame_header.pts = c->pts_counter_audio;
    c->pts_counter_audio += c->samples_per_buffer;
    frame_header.offset = offset;

    avio_write(s->pb, (uint8_t*)&frame_header, sizeof(frame_header));
    avio_flush(s->pb);

    c->write_index_audio = (c->write_index_audio + 1) % NUM_BUFFERS;

    if (sem_post(c->full_sem_audio) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_post(full_sem_audio) failed: %s\n", strerror(errno));
    }

    // Shift the remaining data in the internal buffer to the beginning
    c->internal_buffer_occupancy_audio -= c->frame_buffer_size_audio;
    if (c->internal_buffer_occupancy_audio > 0) {
        memmove(c->internal_buffer_audio, c->internal_buffer_audio + c->frame_buffer_size_audio, c->internal_buffer_occupancy_audio);
    }

    return 0;
}

// write_header: Called when FFmpeg starts writing the output
static int shm_write_header(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    AVStream *st_video = NULL, *st_audio = NULL;
    SHMHeader header = {0};
    pid_t pid = getpid();

    for (int i = 0; i < s->nb_streams; i++) {
        if (s->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            st_video = s->streams[i];
        } else if (s->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            st_audio = s->streams[i];
        }
    }

    if (!st_video && !st_audio) {
        av_log(s, AV_LOG_ERROR, "No audio or video streams were mapped to the SHM muxer.\n");
        return AVERROR(EINVAL);
    }
    
    header.version = 1;
    header.stream_count = s->nb_streams;
    c->pts_counter_audio = 0;

    // Video setup
    if (st_video) {
        snprintf(header.shm_file_video, sizeof(header.shm_file_video), "/goshadertoy_video_%d", pid);
        snprintf(header.empty_sem_name_video, sizeof(header.empty_sem_name_video), "goshadertoy_video_empty_%d", pid);
        snprintf(header.full_sem_name_video, sizeof(header.full_sem_name_video), "goshadertoy_video_full_%d", pid);

        header.width = st_video->codecpar->width;
        header.height = st_video->codecpar->height;
        header.pix_fmt = st_video->codecpar->format;
        header.frame_rate = av_q2d(st_video->r_frame_rate);
        c->frame_buffer_size_video = av_image_get_buffer_size(header.pix_fmt, header.width, header.height, 1);

        size_t required_shm_size_video = sizeof(SHMControlBlock) + (c->frame_buffer_size_video * NUM_BUFFERS);
        c->shm_fd_video = shm_open(header.shm_file_video, O_RDWR | O_CREAT, 0666);
        if (c->shm_fd_video < 0) {
            av_log(s, AV_LOG_ERROR, "Failed to create video shared memory '%s': %s\n", header.shm_file_video, strerror(errno));
            return AVERROR(errno);
        }
        if (ftruncate(c->shm_fd_video, required_shm_size_video) != 0) {
            close(c->shm_fd_video);
            shm_unlink(header.shm_file_video);
            return AVERROR(errno);
        }
        c->buffer_size_video = required_shm_size_video;
        c->buffer_video = mmap(NULL, c->buffer_size_video, PROT_READ | PROT_WRITE, MAP_SHARED, c->shm_fd_video, 0);
        if (c->buffer_video == MAP_FAILED) {
            close(c->shm_fd_video);
            shm_unlink(header.shm_file_video);
            return AVERROR(errno);
        }
        c->control_block_video = (SHMControlBlock*)c->buffer_video;
        c->control_block_video->num_buffers = NUM_BUFFERS;
        c->control_block_video->eof = 0;
        c->write_index_video = 0;
        for(int i=0; i < NUM_BUFFERS; i++) {
            c->frame_buffers_video[i] = c->buffer_video + sizeof(SHMControlBlock) + (i * c->frame_buffer_size_video);
        }

        c->empty_sem_video = sem_open(header.empty_sem_name_video, O_CREAT, 0666, NUM_BUFFERS);
        c->full_sem_video = sem_open(header.full_sem_name_video, O_CREAT, 0666, 0);
    }
    
    // Audio setup
    if (st_audio) {
        snprintf(header.shm_file_audio, sizeof(header.shm_file_audio), "/goshadertoy_audio_%d", pid);
        snprintf(header.empty_sem_name_audio, sizeof(header.empty_sem_name_audio), "goshadertoy_audio_empty_%d", pid);
        snprintf(header.full_sem_name_audio, sizeof(header.full_sem_name_audio), "goshadertoy_audio_full_%d", pid);

        header.sample_rate = st_audio->codecpar->sample_rate;
        header.channels = st_audio->codecpar->ch_layout.nb_channels;
        header.bit_depth = av_get_bytes_per_sample(st_audio->codecpar->format) * 8;
        c->frame_buffer_size_audio = c->samples_per_buffer * header.channels * (header.bit_depth / 8);

        c->internal_buffer_size_audio = c->frame_buffer_size_audio * 2;
        c->internal_buffer_audio = av_malloc(c->internal_buffer_size_audio);
        if (!c->internal_buffer_audio) {
            return AVERROR(ENOMEM);
        }
        c->internal_buffer_occupancy_audio = 0;

        size_t required_shm_size_audio = sizeof(SHMControlBlock) + (c->frame_buffer_size_audio * NUM_BUFFERS);
        c->shm_fd_audio = shm_open(header.shm_file_audio, O_RDWR | O_CREAT, 0666);
        if (c->shm_fd_audio < 0) {
            av_log(s, AV_LOG_ERROR, "Failed to create audio shared memory '%s': %s\n", header.shm_file_audio, strerror(errno));
            return AVERROR(errno);
        }
        if (ftruncate(c->shm_fd_audio, required_shm_size_audio) != 0) {
            close(c->shm_fd_audio);
            shm_unlink(header.shm_file_audio);
            return AVERROR(errno);
        }
        c->buffer_size_audio = required_shm_size_audio;
        c->buffer_audio = mmap(NULL, c->buffer_size_audio, PROT_READ | PROT_WRITE, MAP_SHARED, c->shm_fd_audio, 0);
        if (c->buffer_audio == MAP_FAILED) {
            close(c->shm_fd_audio);
            shm_unlink(header.shm_file_audio);
            return AVERROR(errno);
        }
        c->control_block_audio = (SHMControlBlock*)c->buffer_audio;
        c->control_block_audio->num_buffers = NUM_BUFFERS;
        c->control_block_audio->eof = 0;
        c->write_index_audio = 0;
        for(int i=0; i < NUM_BUFFERS; i++) {
            c->frame_buffers_audio[i] = c->buffer_audio + sizeof(SHMControlBlock) + (i * c->frame_buffer_size_audio);
        }
        
        c->empty_sem_audio = sem_open(header.empty_sem_name_audio, O_CREAT, 0666, NUM_BUFFERS);
        c->full_sem_audio = sem_open(header.full_sem_name_audio, O_CREAT, 0666, 0);
    }
    
    avio_write(s->pb, (uint8_t*)&header, sizeof(header));
    avio_flush(s->pb);
    
    return 0;
}

// write_packet: Called for each packet to be written
static int shm_write_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMMuxerContext *c = s->priv_data;
    FrameHeader frame_header = {0};
    AVStream *st = s->streams[pkt->stream_index];

    if (st->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
        if (sem_wait(c->empty_sem_video) < 0) return AVERROR(errno);

        uint8_t *write_buffer = c->frame_buffers_video[c->write_index_video];
        memcpy(write_buffer, pkt->data, pkt->size);
        
        frame_header.cmdtype = 0;
        frame_header.size = pkt->size;
        frame_header.pts = pkt->pts;
        frame_header.offset = write_buffer - c->buffer_video;

        avio_write(s->pb, (uint8_t*)&frame_header, sizeof(frame_header));
        avio_flush(s->pb);
        
        c->write_index_video = (c->write_index_video + 1) % NUM_BUFFERS;
        if (sem_post(c->full_sem_video) < 0) return AVERROR(errno);
    } else if (st->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
        if (c->internal_buffer_occupancy_audio + pkt->size > c->internal_buffer_size_audio) {
            av_log(s, AV_LOG_ERROR, "Internal audio buffer overflow! Dropping data.\n");
            return 0;
        }

        memcpy(c->internal_buffer_audio + c->internal_buffer_occupancy_audio, pkt->data, pkt->size);
        c->internal_buffer_occupancy_audio += pkt->size;

        while (c->internal_buffer_occupancy_audio >= c->frame_buffer_size_audio) {
            int ret = write_full_audio_frame(s);
            if (ret < 0) {
                return ret;
            }
        }
    }
    
    return 0;
}

static int shm_write_trailer(AVFormatContext *s) {
    SHMMuxerContext *c = s->priv_data;
    if (c->control_block_video) c->control_block_video->eof = 1;
    if (c->control_block_audio) {
        if (c->internal_buffer_occupancy_audio > 0) {
            int padding_size = c->frame_buffer_size_audio - c->internal_buffer_occupancy_audio;
            memset(c->internal_buffer_audio + c->internal_buffer_occupancy_audio, 0, padding_size);
            c->internal_buffer_occupancy_audio += padding_size;
            write_full_audio_frame(s);
        }
        c->control_block_audio->eof = 1;
    }

    FrameHeader eof_header = { .cmdtype = 2 };
    avio_write(s->pb, (uint8_t*)&eof_header, sizeof(eof_header));
    avio_flush(s->pb);
    
    // Cleanup video resources
    if (c->buffer_video) munmap(c->buffer_video, c->buffer_size_video);
    if (c->shm_fd_video > 0) close(c->shm_fd_video);
    // shm_unlink for video should be here
    
    // Cleanup audio resources
    if (c->internal_buffer_audio) av_free(c->internal_buffer_audio);
    if (c->buffer_audio) munmap(c->buffer_audio, c->buffer_size_audio);
    if (c->shm_fd_audio > 0) close(c->shm_fd_audio);
    // shm_unlink for audio should be here

    // Close and unlink semaphores
    if (c->empty_sem_video != SEM_FAILED) {
        sem_close(c->empty_sem_video);
        // sem_unlink for video empty
    }
    if (c->full_sem_video != SEM_FAILED) {
        sem_close(c->full_sem_video);
        // sem_unlink for video full
    }
    if (c->empty_sem_audio != SEM_FAILED) {
        sem_close(c->empty_sem_audio);
        // sem_unlink for audio empty
    }
    if (c->full_sem_audio != SEM_FAILED) {
        sem_close(c->full_sem_audio);
        // sem_unlink for audio full
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
        .video_codec    = AV_CODEC_ID_RAWVIDEO,
    },
    .priv_data_size = sizeof(SHMMuxerContext),
    .write_header   = shm_write_header,
    .write_packet   = shm_write_packet,
    .write_trailer  = shm_write_trailer,
};