package renderer

import (
	inputs "github.com/richinsley/goshadertoy/inputs"
)

type RenderPass struct {
	ShaderProgram         uint32
	Channels              []inputs.IChannel
	Buffer                *inputs.Buffer
	resolutionLoc         int32
	timeLoc               int32
	mouseLoc              int32
	frameLoc              int32
	iChannelLoc           [4]int32
	iChannelResolutionLoc int32
	iDateLoc              int32
	iSampleRateLoc        int32
	iTimeDeltaLoc         int32
	iFrameRateLoc         int32
	iChannelTimeLoc       int32
}
