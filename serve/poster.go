// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/go-avkit/worker/proto"
)

// file is a video file that passed the limits, and what the container says of
// it: the reader is built once and serves the posters asked later.
type file struct {
	data                []byte
	format, mime, codec string
	width, height       int
	duration            float64
	decodable           bool
	reader              *container.Reader
	track               uint32
}

// open reads the description of data, which sniff has accepted.
func open(data []byte, format, mime string, lim proto.Limits) (*file, error) {
	// the reader's own bound on the frames it unpacks is the second line, after
	// the pre-scan: the same limits, a request can only lower them
	r, err := container.NewReaderWithLimits(data, container.ReaderLimits{
		MaxTrackFrames: lim.MaxTrackSamples, MaxFrames: 2 * lim.MaxTrackSamples,
		MaxBoxes: lim.MaxBoxes, MaxBoxDepth: lim.MaxDepth,
	})
	if errors.Is(err, container.ErrReaderLimit) {
		return nil, refuse(proto.CodeLimit, "the %s container is over a reader limit: %v", format, err)
	}
	if err != nil {
		return nil, refuse(proto.CodeSyntax, "the %s container cannot be read: %v", format, err)
	}
	f := &file{data: data, format: format, mime: mime, reader: r}
	cf := r.File()
	for _, t := range cf.Tracks {
		if t.Kind == container.Video {
			f.codec, f.width, f.height, f.track = t.Codec, t.Width, t.Height, t.ID
			break
		}
	}
	if f.track == 0 {
		return nil, refuse(proto.CodeSyntax, "the file has no video track")
	}
	if cf.Timescale > 0 {
		f.duration = float64(cf.Duration) / float64(cf.Timescale)
	}
	f.decodable = f.codec == "avc1" || f.codec == "avc3"
	return f, f.frameWithin(lim)
}

// frameWithin refuses a frame outside the limits.
func (f *file) frameWithin(lim proto.Limits) error {
	if f.width <= 0 || f.height <= 0 || f.width > lim.MaxWidth || f.height > lim.MaxHeight {
		return refuse(proto.CodeLimit, "the frame is %dx%d; at most %dx%d are read", f.width, f.height, lim.MaxWidth, lim.MaxHeight)
	}
	return nil
}

// poster decodes the picture shown at t seconds (the one before it, if none is
// exactly at t; the last one when t is past the end), scaled down to
// lim.PosterWidth, as a JPEG.
func (f *file) poster(t float64, lim proto.Limits) ([]byte, int, int, error) {
	if !f.decodable {
		return nil, 0, 0, refuse(proto.CodeCodec, "the poster of a %s video cannot be decoded (H.264 only)", f.codec)
	}
	if math.IsNaN(t) || math.IsInf(t, 0) {
		return nil, 0, 0, refuse(proto.CodeSyntax, "#t=%g is not a time in the video", t)
	}
	cfg := decode.PosterConfig{
		Config:          decode.Config{MaxFrameMBs: (lim.MaxWidth/16 + 1) * (lim.MaxHeight/16 + 1), MaxSampleBytes: lim.MaxSampleBytes},
		MaxSamples:      lim.MaxSamples,
		MaxTrackSamples: lim.MaxTrackSamples,
	}
	fr, err := decode.PosterFrame(f.reader, f.track, t, cfg)
	if err != nil {
		switch {
		case errors.Is(err, decode.ErrPosterLimit), errors.Is(err, decode.ErrOverLimit):
			return nil, 0, 0, refuse(proto.CodeLimit, "the poster at %gs is over a limit of this worker: %v", t, err)
		case errors.Is(err, decode.ErrUnsupported):
			return nil, 0, 0, refuse(proto.CodeCodec, "the H.264 stream uses a tool that is not decoded: %v", err)
		case errors.Is(err, decode.ErrTime):
			return nil, 0, 0, refuse(proto.CodeSyntax, "#t=%g is not a time in the video", t)
		}
		return nil, 0, 0, refuse(proto.CodeSyntax, "the poster cannot be decoded: %v", err)
	}
	img := toRGBA(fr, lim.PosterWidth)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, 0, 0, refuse(proto.CodeSyntax, "the poster cannot be encoded: %v", err)
	}
	return buf.Bytes(), img.Bounds().Dx(), img.Bounds().Dy(), nil
}

// toRGBA converts a 4:2:0 or 4:0:0 8-bit picture (BT.601 below 720 lines,
// BT.709 above; limited range) and scales it down by averaging boxes to at
// most width pixels across.
func toRGBA(f decode.Frame, width int) *image.RGBA {
	w, h := f.Width, f.Height
	step := 1
	if width > 0 && w > width {
		step = (w + width - 1) / width
	}
	ow, oh := max(w/step, 1), max(h/step, 1)
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	kr, kb := 0.299, 0.114
	if h >= 720 {
		kr, kb = 0.2126, 0.0722
	}
	kg := 1 - kr - kb
	cl := func(v float64) uint8 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v + 0.5)
	}
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var sr, sg, sb, n float64
			for dy := 0; dy < step; dy++ {
				for dx := 0; dx < step; dx++ {
					px, py := x*step+dx, y*step+dy
					if px >= w || py >= h {
						continue
					}
					Y := (float64(f.Y[py*f.YStride+px]) - 16) * 255 / 219
					cb, cr := 0.0, 0.0
					if f.Chroma != decode.Chroma400 && len(f.Cb) > 0 {
						ci := (py/2)*f.CStride + px/2
						if ci < len(f.Cb) && ci < len(f.Cr) {
							cb = (float64(f.Cb[ci]) - 128) * 255 / 224
							cr = (float64(f.Cr[ci]) - 128) * 255 / 224
						}
					}
					r := Y + 2*(1-kr)*cr
					b := Y + 2*(1-kb)*cb
					g := (Y - kr*r - kb*b) / kg
					sr, sg, sb, n = sr+r, sg+g, sb+b, n+1
				}
			}
			if n == 0 {
				continue
			}
			out.SetRGBA(x, y, color.RGBA{cl(sr / n), cl(sg / n), cl(sb / n), 255})
		}
	}
	return out
}
