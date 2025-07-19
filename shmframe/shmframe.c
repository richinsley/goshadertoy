#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <semaphore.h>

#include "shmframe.h"
#include "protocol.h"

#include "libavutil/avstring.h"
#include "libavutil/imgutils.h"
#include "libavutil/pixdesc.h"
#include "libavformat/avformat.h"
#include "libavformat/internal.h"
#include "libavutil/intreadwrite.h"
#include "libavcodec/avcodec.h"

typedef struct {
    uint8_t *shm_buffer; // Pointer to the start of the shared memory region
    int shm_buffer_size;
    int shm_fd;
    sem_t *empty_sem;
    sem_t *full_sem;
    SHMControlBlock *control_block;
} SHMDemuxerContext;

static int shm_read_header(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    SHMHeader header;
    AVStream *st;

    if (avio_read(s->pb, (unsigned char*)&header, sizeof(header)) != sizeof(header)) {
        av_log(s, AV_LOG_ERROR, "Failed to read initial SHMHeader from pipe.\n");
        return AVERROR(EIO);
    }

    av_log(s, AV_LOG_INFO, "shm demuxer: Read header from pipe: shm_file='%s', bit_depth=%u, width=%u, height=%u\n",
           header.shm_file, header.bit_depth, header.width, header.height);

    c->shm_fd = shm_open(header.shm_file, O_RDONLY, 0);
    if (c->shm_fd < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to open shared memory '%s': %s\n", header.shm_file, strerror(errno));
        return AVERROR(errno);
    }

    c->empty_sem = sem_open(header.empty_sem_name, 0);
    if (c->empty_sem == SEM_FAILED) {
        av_log(s, AV_LOG_ERROR, "Failed to open empty semaphore '%s': %s\n", header.empty_sem_name, strerror(errno));
        close(c->shm_fd);
        return AVERROR(errno);
    }

    c->full_sem = sem_open(header.full_sem_name, 0);
    if (c->full_sem == SEM_FAILED) {
        av_log(s, AV_LOG_ERROR, "Failed to open full semaphore '%s': %s\n", header.full_sem_name, strerror(errno));
        sem_close(c->empty_sem);
        close(c->shm_fd);
        return AVERROR(errno);
    }

    struct stat st_shm;
    if (fstat(c->shm_fd, &st_shm) < 0) {
        close(c->shm_fd);
        av_log(s, AV_LOG_ERROR, "fstat on shared memory failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }
    c->shm_buffer_size = st_shm.st_size;

    c->shm_buffer = mmap(NULL, c->shm_buffer_size, PROT_READ, MAP_SHARED, c->shm_fd, 0);
    if (c->shm_buffer == MAP_FAILED) {
        close(c->shm_fd);
        av_log(s, AV_LOG_ERROR, "mmap failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }

    c->control_block = (SHMControlBlock*)c->shm_buffer;

    st = avformat_new_stream(s, NULL);
    if (!st) {
        munmap(c->shm_buffer, c->shm_buffer_size);
        close(c->shm_fd);
        return AVERROR(ENOMEM);
    }

    // Configure the stream based on the header from the pipe
    if (header.frametype == 0) { // Video stream
        av_log(s, AV_LOG_INFO, "shm demuxer: Configuring for video stream.\n");
        st->time_base = av_inv_q(av_d2q(header.frame_rate, 1000000));
        st->r_frame_rate = av_d2q(header.frame_rate, 1000000);
        st->avg_frame_rate = st->r_frame_rate;
        st->codecpar->codec_type = AVMEDIA_TYPE_VIDEO;
        st->codecpar->codec_id = AV_CODEC_ID_RAWVIDEO;
        st->codecpar->width = header.width;
        st->codecpar->height = header.height;
        st->codecpar->format = header.pix_fmt;
    } else { // Audio stream (unchanged)
        // ... audio configuration logic ...
    }

    
    av_log(s, AV_LOG_INFO, "shm demuxer header read successfully.\n");
    return 0;
}

static int shm_read_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMDemuxerContext *c = s->priv_data;
    FrameHeader frame_header;
    int ret;

    ret = avio_read(s->pb, (unsigned char*)&frame_header, sizeof(frame_header));
    if (ret != sizeof(frame_header)) {
        return AVERROR_EOF;
    }

    if (frame_header.cmdtype == 2) { // EOF
        return AVERROR_EOF;
    }

    if (sem_wait(c->full_sem) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_wait(full_sem) failed: %s\n", strerror(errno));
        return AVERROR(errno);
    }

    if (frame_header.offset + frame_header.size > c->shm_buffer_size) {
        av_log(s, AV_LOG_ERROR, "Frame offset+size (%llu) exceeds shared memory size (%d).\n", (unsigned long long)frame_header.offset + frame_header.size, c->shm_buffer_size);
        sem_post(c->empty_sem);
        return AVERROR(EINVAL);
    }
    
    ret = av_new_packet(pkt, frame_header.size);
    if (ret < 0) {
        sem_post(c->empty_sem);
        return ret;
    }

    memcpy(pkt->data, c->shm_buffer + frame_header.offset, frame_header.size);
    pkt->pts = frame_header.pts;
    pkt->dts = pkt->pts;
    pkt->stream_index = 0;
    pkt->flags |= AV_PKT_FLAG_KEY;

    if (sem_post(c->empty_sem) < 0) {
        av_log(s, AV_LOG_ERROR, "sem_post(empty_sem) failed: %s\n", strerror(errno));
    }

    return frame_header.size;
}

static int shm_read_close(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    if (c->shm_buffer) {
        munmap(c->shm_buffer, c->shm_buffer_size);
    }
    if (c->shm_fd >= 0) {
        close(c->shm_fd);
    }
    if (c->empty_sem != SEM_FAILED) {
        sem_close(c->empty_sem);
    }
    if (c->full_sem != SEM_FAILED) {
        sem_close(c->full_sem);
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