#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <semaphore.h>

#include "shmframe.h"
#include "protocol.h"

#include "libavutil/avstring.h"
#include "libavutil/channel_layout.h"
#include "libavutil/imgutils.h"
#include "libavutil/pixdesc.h"
#include "libavformat/avformat.h"
#include "libavformat/internal.h"
#include "libavutil/intreadwrite.h"
#include "libavcodec/avcodec.h"
#include "libavutil/time.h" // Required for timing functions

typedef struct {
    uint8_t *shm_buffer_video;
    int shm_buffer_size_video;
    int shm_fd_video;
    sem_t *empty_sem_video;
    sem_t *full_sem_video;
    SHMControlBlock *control_block_video;

    uint8_t *shm_buffer_audio;
    int shm_buffer_size_audio;
    int shm_fd_audio;
    sem_t *empty_sem_audio;
    sem_t *full_sem_audio;
    SHMControlBlock *control_block_audio;

    // --- Metrics ---
    int64_t metrics_last_time;
    int metrics_frame_count;
    int metrics_total_samples;
} SHMDemuxerContext;

static int shm_read_header(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    SHMHeader header;
    AVStream *st_video = NULL, *st_audio = NULL;

    if (avio_read(s->pb, (unsigned char*)&header, sizeof(header)) != sizeof(header)) {
        av_log(s, AV_LOG_ERROR, "Failed to read initial SHMHeader from pipe.\n");
        return AVERROR(EIO);
    }
    
    // Initialize metrics
    c->metrics_last_time = -1;
    c->metrics_frame_count = 0;
    c->metrics_total_samples = 0;

    // Video Setup (only if video parameters are present)
    if (header.width > 0 && header.height > 0) {
        c->shm_fd_video = shm_open(header.shm_file_video, O_RDONLY, 0);
        if (c->shm_fd_video < 0) {
            av_log(s, AV_LOG_ERROR, "Failed to open video shared memory '%s': %s\n", header.shm_file_video, strerror(errno));
            return AVERROR(errno);
        }

        c->empty_sem_video = sem_open(header.empty_sem_name_video, 0);
        if (c->empty_sem_video == SEM_FAILED) {
            av_log(s, AV_LOG_ERROR, "Failed to open video empty semaphore '%s': %s\n", header.empty_sem_name_video, strerror(errno));
            close(c->shm_fd_video);
            return AVERROR(errno);
        }

        c->full_sem_video = sem_open(header.full_sem_name_video, 0);
        if (c->full_sem_video == SEM_FAILED) {
            av_log(s, AV_LOG_ERROR, "Failed to open video full semaphore '%s': %s\n", header.full_sem_name_video, strerror(errno));
            sem_close(c->empty_sem_video);
            close(c->shm_fd_video);
            return AVERROR(errno);
        }

        struct stat st_shm_video;
        if (fstat(c->shm_fd_video, &st_shm_video) < 0) {
            av_log(s, AV_LOG_ERROR, "fstat on video shared memory failed: %s\n", strerror(errno));
            close(c->shm_fd_video);
            return AVERROR(errno);
        }
        c->shm_buffer_size_video = st_shm_video.st_size;

        c->shm_buffer_video = mmap(NULL, c->shm_buffer_size_video, PROT_READ, MAP_SHARED, c->shm_fd_video, 0);
        if (c->shm_buffer_video == MAP_FAILED) {
            av_log(s, AV_LOG_ERROR, "mmap for video failed: %s\n", strerror(errno));
            close(c->shm_fd_video);
            return AVERROR(errno);
        }
        c->control_block_video = (SHMControlBlock*)c->shm_buffer_video;

        st_video = avformat_new_stream(s, NULL);
        if (!st_video) {
            munmap(c->shm_buffer_video, c->shm_buffer_size_video);
            close(c->shm_fd_video);
            return AVERROR(ENOMEM);
        }
        st_video->time_base = av_inv_q(av_d2q(header.frame_rate, 1000000));
        st_video->r_frame_rate = av_d2q(header.frame_rate, 1000000);
        st_video->avg_frame_rate = st_video->r_frame_rate;
        st_video->codecpar->codec_type = AVMEDIA_TYPE_VIDEO;
        st_video->codecpar->codec_id = AV_CODEC_ID_RAWVIDEO;
        st_video->codecpar->width = header.width;
        st_video->codecpar->height = header.height;
        st_video->codecpar->format = header.pix_fmt;
    }

    // Audio Setup (only if audio parameters are present)
    if (header.sample_rate > 0 && header.channels > 0) {
        c->shm_fd_audio = shm_open(header.shm_file_audio, O_RDONLY, 0);
        if (c->shm_fd_audio < 0) {
            av_log(s, AV_LOG_ERROR, "Failed to open audio shared memory '%s': %s\n", header.shm_file_audio, strerror(errno));
            return AVERROR(errno);
        }

        c->empty_sem_audio = sem_open(header.empty_sem_name_audio, 0);
        if (c->empty_sem_audio == SEM_FAILED) {
            av_log(s, AV_LOG_ERROR, "Failed to open audio empty semaphore '%s': %s\n", header.empty_sem_name_audio, strerror(errno));
            close(c->shm_fd_audio);
            return AVERROR(errno);
        }

        c->full_sem_audio = sem_open(header.full_sem_name_audio, 0);
        if (c->full_sem_audio == SEM_FAILED) {
            av_log(s, AV_LOG_ERROR, "Failed to open audio full semaphore '%s': %s\n", header.full_sem_name_audio, strerror(errno));
            sem_close(c->empty_sem_audio);
            close(c->shm_fd_audio);
            return AVERROR(errno);
        }

        struct stat st_shm_audio;
        if (fstat(c->shm_fd_audio, &st_shm_audio) < 0) {
            av_log(s, AV_LOG_ERROR, "fstat on audio shared memory failed: %s\n", strerror(errno));
            close(c->shm_fd_audio);
            return AVERROR(errno);
        }
        c->shm_buffer_size_audio = st_shm_audio.st_size;

        c->shm_buffer_audio = mmap(NULL, c->shm_buffer_size_audio, PROT_READ, MAP_SHARED, c->shm_fd_audio, 0);
        if (c->shm_buffer_audio == MAP_FAILED) {
            av_log(s, AV_LOG_ERROR, "mmap for audio failed: %s\n", strerror(errno));
            close(c->shm_fd_audio);
            return AVERROR(errno);
        }
        c->control_block_audio = (SHMControlBlock*)c->shm_buffer_audio;

        st_audio = avformat_new_stream(s, NULL);
        if (!st_audio) {
            munmap(c->shm_buffer_audio, c->shm_buffer_size_audio);
            close(c->shm_fd_audio);
            return AVERROR(ENOMEM);
        }
        st_audio->codecpar->codec_type = AVMEDIA_TYPE_AUDIO;
        st_audio->codecpar->codec_id = AV_CODEC_ID_PCM_F32LE;
        st_audio->codecpar->format = AV_SAMPLE_FMT_FLT;
        st_audio->codecpar->sample_rate = header.sample_rate;
        av_channel_layout_default(&st_audio->codecpar->ch_layout, header.channels);
        st_audio->time_base = (AVRational){1, st_audio->codecpar->sample_rate};
    }

    return 0;
}

static int shm_read_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMDemuxerContext *c = s->priv_data;
    FrameHeader frame_header;
    int ret;

    // --- Metrics Start ---
    if (c->metrics_last_time == -1) {
        c->metrics_last_time = av_gettime_relative();
    }
    // --- Metrics End ---

    ret = avio_read(s->pb, (unsigned char*)&frame_header, sizeof(frame_header));
    if (ret != sizeof(frame_header)) {
        return AVERROR_EOF;
    }

    if (frame_header.cmdtype == 2) { // EOF
        return AVERROR_EOF;
    }

    if (frame_header.cmdtype == 0) { // Video
        if (sem_wait(c->full_sem_video) < 0) {
            return AVERROR(errno);
        }
        if (frame_header.offset + frame_header.size > c->shm_buffer_size_video) {
            sem_post(c->empty_sem_video);
            return AVERROR(EINVAL);
        }
        ret = av_new_packet(pkt, frame_header.size);
        if (ret < 0) {
            sem_post(c->empty_sem_video);
            return ret;
        }
        memcpy(pkt->data, c->shm_buffer_video + frame_header.offset, frame_header.size);
        pkt->stream_index = 0; // Video stream
        if (sem_post(c->empty_sem_video) < 0) {
            av_log(s, AV_LOG_ERROR, "sem_post(empty_sem_video) failed: %s\n", strerror(errno));
        }
    } else if (frame_header.cmdtype == 1) { // Audio
        if (sem_wait(c->full_sem_audio) < 0) {
            return AVERROR(errno);
        }
        if (frame_header.offset + frame_header.size > c->shm_buffer_size_audio) {
            sem_post(c->empty_sem_audio);
            return AVERROR(EINVAL);
        }
        ret = av_new_packet(pkt, frame_header.size);
        if (ret < 0) {
            sem_post(c->empty_sem_audio);
            return ret;
        }
        memcpy(pkt->data, c->shm_buffer_audio + frame_header.offset, frame_header.size);
        pkt->stream_index = s->nb_streams - 1; // Audio stream is always last
        if (sem_post(c->empty_sem_audio) < 0) {
            av_log(s, AV_LOG_ERROR, "sem_post(empty_sem_audio) failed: %s\n", strerror(errno));
        }
        c->metrics_total_samples += frame_header.size / 4;
    }

    pkt->pts = frame_header.pts;
    pkt->dts = pkt->pts;

    c->metrics_frame_count++;
    
    int64_t now = av_gettime_relative();
    if (now - c->metrics_last_time >= 1000000) { // 1 second in microseconds
        av_log(s, AV_LOG_DEBUG, "[METRICS] Demuxer Rate: %d fps, %d samples/sec\n", 
               c->metrics_frame_count, c->metrics_total_samples);
        c->metrics_frame_count = 0;
        c->metrics_total_samples = 0;
        c->metrics_last_time = now;
    }

    return 0; 
}

static int shm_read_close(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    if (c->shm_buffer_video) {
        munmap(c->shm_buffer_video, c->shm_buffer_size_video);
    }
    if (c->shm_fd_video >= 0) {
        close(c->shm_fd_video);
    }
    if (c->empty_sem_video != SEM_FAILED) {
        sem_close(c->empty_sem_video);
    }
    if (c->full_sem_video != SEM_FAILED) {
        sem_close(c->full_sem_video);
    }

    if (c->shm_buffer_audio) {
        munmap(c->shm_buffer_audio, c->shm_buffer_size_audio);
    }
    if (c->shm_fd_audio >= 0) {
        close(c->shm_fd_audio);
    }
    if (c->empty_sem_audio != SEM_FAILED) {
        sem_close(c->empty_sem_audio);
    }
    if (c->full_sem_audio != SEM_FAILED) {
        sem_close(c->full_sem_audio);
    }
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