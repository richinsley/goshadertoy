#ifndef PROTOCOL_H
#define PROTOCOL_H

#include <stdint.h>

/**
 * @brief Control block for the shared memory ring buffer.
 *
 * This structure is placed at the beginning of the shared memory region
 * and is used to synchronize the producer and consumer.
 */
typedef struct {
    // The total number of buffer slots.
    uint32_t num_buffers;

    // A flag to signal the end of the stream.
    volatile uint32_t eof;
} SHMControlBlock;


/**
 * @brief Header written once at the beginning of the pipe.
 *
 * This structure contains the video stream's properties.
 */
typedef struct {
    char shm_file[512];
    char empty_sem_name[256];
    char full_sem_name[256];
    uint32_t version;
    uint32_t frametype; // 0 for video, 1 for audio
    uint32_t frame_rate; // Frame rate in frames per second.
    uint32_t channels;   // Number of audio channels (0 for video).
    uint32_t sample_rate; // Audio sample rate (0 for video).
    uint32_t bit_depth;
    uint32_t width;
    uint32_t height;
    int32_t  pix_fmt;     // The AVPixelFormat enum value for the pixel format.
} SHMHeader;

/**
 * @brief Header written before each frame in the pipe.
 *
 * This structure contains the size and timestamp of the following frame.
 */
typedef struct {
    uint32_t cmdtype; // 0 for video, 1 for audio, 2 for EOF
    uint32_t size;
    int64_t pts;
    uint64_t offset; // The exact byte offset for the frame in shared memory
} FrameHeader;

#endif // PROTOCOL_H