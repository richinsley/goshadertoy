package renderer

type ShaderOptions struct {
	APIKey     *string
	ShaderID   *string
	Help       *bool
	Record     *bool
	Duration   *float64
	FPS        *int
	Width      *int
	Height     *int
	BitDepth   *int
	OutputFile *string
	FFMPEGPath *string
}
