package renderer

type ShaderOptions struct {
	APIKey         *string
	ShaderID       *string
	Help           *bool
	Mode           *string
	Duration       *float64
	FPS            *int
	Width          *int
	Height         *int
	BitDepth       *int
	OutputFile     *string
	FFMPEGPath     *string
	DecklinkDevice *string
	Codec          *string
}
