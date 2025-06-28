package inputs

// Uniforms holds the global shader values that dynamic channels might need.
type Uniforms struct {
	Time  float32
	Mouse [4]float32
	Frame int32 // Frame count for animations or effects
	// Add other uniforms like Date, Frame, etc., as needed.
}

// IChannel defines the contract for any Shadertoy input channel (iChannel0-3).
type IChannel interface {
	// GetCType return the ctype of the input
	GetCType() string

	// Update is called once per frame, passing in the global uniforms.
	Update(uniforms *Uniforms)

	// GetTextureID returns the OpenGL texture ID that should be bound.
	GetTextureID() uint32

	// ChannelRes returns the resolution of the input channel as a vec3.
	ChannelRes() [3]float32

	// Destroy releases any resources held by the channel.
	Destroy()

	// GetSamplerType returns the GLSL sampler type (e.g., "sampler2D", "samplerCube").
	GetSamplerType() string
}
