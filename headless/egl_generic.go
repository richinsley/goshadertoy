//go:build !linux

package headless

import (
	"fmt"

	"github.com/richinsley/goshadertoy/graphics"
)

func NewHeadless(width, height int) (graphics.Context, error) {
	return nil, fmt.Errorf("egl headless rendering is not supported on this platform")
}
