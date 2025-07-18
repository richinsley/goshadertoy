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

#define NUM_BUFFERS 3

typedef struct {
    uint8_t *buffer; // Pointer to the start of the shared memory region
    int buffer_size;
    int shm_fd;
    int current_stream_index;
    SHMControlBlock *control_block;
    uint8_t *frame_buffers[NUM_BUFFERS];
    size_t frame_buffer_size;
} SHMDemuxerContext;

static int shm_read_header(AVFormatContext *s) {
    SHMDemuxerContext *c = s->priv_data;
    SHMHeader header;
    AVStream *st;

    // Read the initial header from the pipe to get stream properties and shm name
    if (avio_read(s->pb, (unsigned char*)&header, sizeof(header)) != sizeof(header)) {
        av_log(s, AV_LOG_ERROR, "Failed to read initial SHMHeader from pipe.\n");
        return AVERROR(EIO);
    }

    av_log(s, AV_LOG_INFO, "shm demuxer: Read header from pipe: shm_file='%s', version=%u, frametype=%u, frame_rate=%u, channels=%u, sample_rate=%u, bit_depth=%u, width=%u, height=%u, pix_fmt=%d\n",
           header.shm_file, header.version, header.frametype,
           header.frame_rate, header.channels, header.sample_rate,
           header.bit_depth, header.width, header.height, header.pix_fmt);

    c->shm_fd = shm_open(header.shm_file, O_RDWR, 0666);
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

    // The control block is at the beginning of the shared memory
    c->control_block = (SHMControlBlock*)c->buffer;
    c->frame_buffer_size = (c->buffer_size - sizeof(SHMControlBlock)) / c->control_block->num_buffers;

    // Get pointers to the start of each frame slot in the ring buffer
    for(unsigned int i = 0; i < c->control_block->num_buffers; i++) {
        c->frame_buffers[i] = c->buffer + sizeof(SHMControlBlock) + (i * c->frame_buffer_size);
    }

    st = avformat_new_stream(s, NULL);
    if (!st) {
        munmap(c->buffer, c->buffer_size);
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
    
    c->current_stream_index = 0;
    av_log(s, AV_LOG_INFO, "shm demuxer header read successfully.\n");
    return 0;
}

static int shm_read_packet(AVFormatContext *s, AVPacket *pkt) {
    SHMDemuxerContext *c = s->priv_data;
    FrameHeader frame_header;
    int ret;

    // Blocking read from the pipe. This is our primary signal for a new frame.
    av_log(s, AV_LOG_INFO, "shm demuxer try to read a packet.\n");
    ret = avio_read(s->pb, (unsigned char*)&frame_header, sizeof(frame_header));
    av_log(s, AV_LOG_INFO, "shm demuxer read a packet.\n");
    if (ret != sizeof(frame_header)) {
        // This indicates EOF or an error on the pipe
        return AVERROR_EOF;
    }

    if (frame_header.cmdtype == 2) { // Explicit EOF command
        av_log(s, AV_LOG_INFO, "Received explicit EOF command. Shutting down.\n");
        return AVERROR_EOF;
    }

    // We have a header, so data should be ready in the ring buffer.
    // As a safeguard, we check. This should not normally block.
    while (c->control_block->read_index == c->control_block->write_index) {
        if (c->control_block->eof) return AVERROR_EOF;
        usleep(100); // Spin-wait very briefly
    }

    if (frame_header.size > c->frame_buffer_size) {
        av_log(s, AV_LOG_ERROR, "Frame size (%d) exceeds shared memory buffer slot size (%zu).\n", frame_header.size, c->frame_buffer_size);
        return AVERROR(EINVAL);
    }
    
    ret = av_new_packet(pkt, frame_header.size);
    if (ret < 0) {
        av_log(s, AV_LOG_ERROR, "Failed to allocate new packet.\n");
        return ret;
    }


    uint8_t *read_buffer = c->frame_buffers[c->control_block->read_index];
    memcpy(pkt->data, read_buffer, frame_header.size);
    pkt->pts = frame_header.pts;
    pkt->dts = pkt->pts;
    pkt->stream_index = c->current_stream_index;
    pkt->flags |= AV_PKT_FLAG_KEY;

    av_log(s, AV_LOG_INFO, "shm demuxer passed safeguard check.\n");
    
    // Signal that we're done with this buffer slot by advancing the read index
    c->control_block->read_index = (c->control_block->read_index + 1) % c->control_block->num_buffers;

    av_log(s, AV_LOG_INFO, "shm demuxer read packet: pts=%" PRId64 ", dts=%" PRId64 ", size=%d\n", pkt->pts, pkt->dts, frame_header.size);

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