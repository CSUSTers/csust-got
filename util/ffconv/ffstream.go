package ffconv

import ff "github.com/u2takey/ffmpeg-go"

// StreamApply : [*ff.Stream] => [*ff.Stream]
type StreamApply func(*ff.Stream) *ff.Stream

// Combine combine two [StreamApply]
func (f StreamApply) Combine(f2 StreamApply) StreamApply {
	return func(input *ff.Stream) *ff.Stream {
		return f2(f(input))
	}
}

// NewPipeInputStream returns a [StreamApply] that create a pipe input stream
func NewPipeInputStream(format string) StreamApply {
	return func(_ *ff.Stream) *ff.Stream {
		return GetPipeInputStream(format)
	}
}

// WithStream returns a [StreamApply] with an existing [ff.Stream]
func WithStream(s *ff.Stream) StreamApply {
	return func(_ *ff.Stream) *ff.Stream {
		return s
	}
}

var _ StreamApply = NewPipeInputStream("")

// GetPipeInputStream get pipe input stream
func GetPipeInputStream(fileType string) *ff.Stream {
	inputArgs := ff.KwArgs{}
	if fileType != "" {
		inputArgs["f"] = fileType
	}
	input := ff.Input("pipe:", inputArgs)
	return input
}
