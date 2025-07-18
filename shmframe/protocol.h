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
    // Index of the buffer slot the producer is currently writing to.
    uint32_t write_index;

    // Index of the buffer slot the consumer is currently reading from.
    uint32_t read_index;
    
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
} FrameHeader;

#endif // PROTOCOL_H