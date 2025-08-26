module github.com/richinsley/goshadertoy

go 1.24.0

replace github.com/go-gl/glfw/v3.3/glfw => ./glfw/v3.3/glfw

require (
	github.com/go-gl/gl v0.0.0-20231021071112-07e5d0ea2e71
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20250301202403-da16c1255728
	github.com/mjibson/go-dsp v0.0.0-20180508042940-11479a337f12
	github.com/richinsley/goshadertranslator v1.0.2
	golang.org/x/sys v0.0.0-20200930185726-fdedc70b468f
)

require github.com/tetratelabs/wazero v1.9.0 // indirect
