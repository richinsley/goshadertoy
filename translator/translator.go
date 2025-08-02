package translator

import (
	"context"

	gst "github.com/richinsley/goshadertranslator"
)

var translator *gst.ShaderTranslator

func GetTranslator() *gst.ShaderTranslator {
	if translator == nil {
		ctx := context.Background()
		translator, _ = gst.NewShaderTranslator(ctx)
	}
	return translator
}
