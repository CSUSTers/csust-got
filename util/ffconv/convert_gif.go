package ffconv

import (
	"io"

	ff "github.com/u2takey/ffmpeg-go"
)

// Convert2GifFromReader read media file from reader `r` and convert it to gif
// return the converted data reader and a channel of run error
func (c *FFConv) Convert2GifFromReader(r io.Reader, inputFileType string) (io.Reader, <-chan error) {
	in := NewPipeInputStream(inputFileType).Combine(GifPaletteVfStream)

	outputArg := ff.KwArgs{
		"c:v": "gif",
		"f":   "gif",
	}
	return c.ConvertPipe2Pipe(r, in, outputArg)
}

// GifPaletteVfStream get gif palette vfilter stream
func GifPaletteVfStream(input *ff.Stream) *ff.Stream {
	split := input.Split()
	ori, s1 := split.Get("ori"), split.Get("s1")
	p1 := s1.Filter("palettegen", ff.Args{}, ff.KwArgs{
		"reserve_transparent": "on",
		"transparency_color":  "ffffff",
		"stats_mode":          "diff",
	})

	vf := ff.Filter([]*ff.Stream{ori, p1}, "paletteuse", ff.Args{}, ff.KwArgs{"dither": "sierra3"})
	return vf
}
