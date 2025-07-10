#ifndef PROTOCOL_H
#define PROTOCOL_H

#include <stdint.h>

/**
 * @brief Header written once at the beginning of the shared memory.
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
    uint32_t bit_depth;  // Audio bit depth (0 for video).
    uint32_t width;
    uint32_t height;
    int32_t  pix_fmt;     // The AVPixelFormat enum value for the pixel format.
} SHMHeader;

/**
 * @brief Header written before each frame in the shared memory.
 *
 * This structure contains the size and timestamp of the following frame.
 */
typedef struct {
    uint32_t cmdtype; // 0 for video, 1 for audio, 2 for EOF
    uint32_t size;
    int64_t pts;
} FrameHeader;

#endif // PROTOCOL_H