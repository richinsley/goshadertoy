package inputs

import (
	"log"

	api "github.com/richinsley/goshadertoy/api"
)

func GetChannels(shaderArgs *api.ShaderArgs) ([]IChannel, error) {
	// Create IChannel objects from shader arguments
	channels := make([]IChannel, 4)
	for i, chInput := range shaderArgs.Inputs {
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
				log.Printf("Warning: Channel %d is a texture but has no image data, skipping.", i)
				continue
			}
			imgChannel, err := NewImageChannel(chInput.Channel, chInput.Data, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create image channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = imgChannel
			log.Printf("Initialized ImageChannel %d.", chInput.Channel)
		case "volume":
			if chInput.Volume == nil {
				log.Printf("Warning: Channel %d is a volume but has no data, skipping.", channelIndex)
				continue
			}
			volChannel, err := NewVolumeChannel(channelIndex, chInput.Volume, chInput.Sampler)
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
				log.Printf("Warning: Channel %d is a cubemap but is missing image data, skipping.", i)
				continue
			}
			cubeChannel, err := NewCubeMapChannel(chInput.Channel, chInput.CubeData, chInput.Sampler)
			if err != nil {
				log.Fatalf("Failed to create cube map channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = cubeChannel
			log.Printf("Initialized CubeMapChannel %d.", chInput.Channel)
		case "buffer":
			log.Printf("Warning: Buffer inputs are not yet supported (Channel %d).", i)
		case "mic":
			newChannel, err := NewMicChannel(chInput.Channel)
			if err != nil {
				log.Fatalf("Failed to create image channel %d: %v", chInput.Channel, err)
			}
			channels[chInput.Channel] = newChannel
			log.Printf("Initialized MicChannel %d.", chInput.Channel)
		default:
			if chInput.CType != "" {
				log.Printf("Warning: Unsupported channel type '%s' for channel %d.", chInput.CType, i)
			}
		}
	}
	return channels, nil
}
