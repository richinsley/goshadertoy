package inputs

import (
	"log"

	api "github.com/richinsley/goshadertoy/api"
	options "github.com/richinsley/goshadertoy/options"
)

func GetChannels(shaderInputs []*api.ShadertoyChannel, width, height int, vao uint32, buffers map[string]*Buffer, options *options.ShaderOptions) ([]IChannel, error) {
	// Create IChannel objects from shader arguments
	channels := make([]IChannel, 4)
	for _, chInput := range shaderInputs {
		if chInput == nil {
			continue
		}

		channelIndex := chInput.Channel
		// Ensure channel index is valid
		if channelIndex < 0 || channelIndex >= 4 {
			log.Printf("Warning: Invalid channel index %d found, skipping.", channelIndex)
			continue
		}

		switch chInput.CType {
		case "texture":
			if chInput.Data == nil {
				log.Printf("Warning: Channel %d is a texture but has no image data, skipping.", channelIndex)
				continue
			}
			imgChannel, err := NewImageChannel(chInput.Data, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create image channel %d: %v", channelIndex, err)
			}
			channels[channelIndex] = imgChannel
			log.Printf("Initialized ImageChannel %d.", channelIndex)
		case "volume":
			if chInput.Volume == nil {
				log.Printf("Warning: Channel %d is a volume but has no data, skipping.", channelIndex)
				continue
			}
			volChannel, err := NewVolumeChannel(chInput.Volume, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create volume channel %d: %v", channelIndex, err)
			}
			channels[channelIndex] = volChannel
			log.Printf("Initialized VolumeChannel %d.", channelIndex)
		case "cubemap":
			isComplete := true
			for _, img := range chInput.CubeData {
				if img == nil {
					isComplete = false
					break
				}
			}
			if !isComplete {
				log.Printf("Warning: Channel %d is a cubemap but is missing image data, skipping.", channelIndex)
				continue
			}
			cubeChannel, err := NewCubeMapChannel(chInput.CubeData, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create cube map channel %d: %v", channelIndex, err)
			}
			channels[channelIndex] = cubeChannel
			log.Printf("Initialized CubeMapChannel %d.", channelIndex)
		case "buffer":
			// Look up the buffer in the provided map
			buffer, ok := buffers[chInput.BufferRef]
			if !ok {
				log.Fatalf("Buffer %s not found for channel %d", chInput.BufferRef, channelIndex)
			}
			// update the buffer's filter and wrap modes
			buffer.UpdateTextureParameters(chInput.Sampler.Wrap, chInput.Sampler.Filter, chInput.Sampler)

			channels[channelIndex] = buffer
			log.Printf("Assigned Buffer %s to Channel %d.", chInput.BufferRef, channelIndex)
		case "mic":
			var newChannel IChannel
			var err error
			if options != nil && *options.AudioInputDevice != "" {
				// Use FFmpeg if the audio-input flag is set
				newChannel, err = NewMicChannelWithFFmpeg(options, chInput.Sampler)
			} else {
				// Fallback to the default portaudio microphone
				newChannel, err = NewMicChannel(options, chInput.Sampler)
			}

			if err != nil {
				log.Fatalf("Failed to create mic channel: %v", err)
			}
			channels[channelIndex] = newChannel
			log.Printf("Initialized MicChannel %d.", channelIndex)
		case "music":
			if *options.AudioInputDevice == "" && *options.AudioInputFile == "" {
				*options.AudioInputFile = chInput.MusicFile
			}
			// Use FFmpeg if the audio-input flag is set
			newChannel, err := NewMicChannelWithFFmpeg(options, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create mic channel: %v", err)
			}
			channels[channelIndex] = newChannel
			log.Printf("Initialized MusicChannel %d.", channelIndex)
		default:
			if chInput.CType != "" {
				log.Printf("Warning: Unsupported channel type '%s' for channel %d.", chInput.CType, channelIndex)
			}
		}
	}
	return channels, nil
}
