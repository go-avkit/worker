// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"bytes"
	"image/jpeg"
	"math"
	"testing"

	"github.com/go-avkit/worker/proto"
)

// FuzzHandle feeds Handle hostile requests (any operation, key, time and ALL
// NINE limits) with hostile payloads (seeded from real files and from the
// laced-block bomb). Whatever comes in: no panic, an answer that carries the
// request's ID, a refusal that has a known code and a message, a successful
// poster that is a JPEG of the size it says, and sizes within the worker's own
// bounds, which no request raises. The corpus in testdata/fuzz runs as ordinary
// tests.
func FuzzHandle(f *testing.F) {
	for _, n := range []string{"bframes.mp4", "bframes.mkv", "hvc1.mp4"} {
		d := read(f, n)
		f.Add(d, "poster", 0.5, true, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		f.Add(d, "probe", 0.0, true, 1, 3, 1, 8, 8, 1, 1, 1, 1)
	}
	f.Add([]byte(nil), "poster", -1.0, false, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	m := math.MaxInt
	f.Add(ftyp, "poster", 1e300, true, m, m, m, m, m, m, m, m, m)
	f.Add([]byte("hello"), "\xff\x00", 0.0, true, -5, -5, -5, -5, -5, -5, -5, -5, -5)
	f.Add(laced(2000, 256, 1, false), "probe", 0.0, true, m, m, m, m, m, m, m, m, m)
	f.Add(laced(300, 256, 2, true), "poster", 0.0, true, 0, 0, 0, 0, 0, 0, 5000, 0, 0)
	known := map[string]bool{proto.CodeLimit: true, proto.CodeSyntax: true, proto.CodeCodec: true, proto.CodeNotHeld: true}
	s := &Server{}
	f.Fuzz(func(t *testing.T, data []byte, op string, tm float64, hasData bool,
		maxBytes, maxBoxes, maxDepth, maxWidth, maxHeight, maxSamples, maxTrackSamples, maxSampleBytes, posterW int) {
		if len(data) > 1<<20 {
			return
		}
		ask := proto.Limits{
			MaxBytes: maxBytes, MaxBoxes: maxBoxes, MaxDepth: maxDepth, MaxWidth: maxWidth, MaxHeight: maxHeight,
			MaxSamples: maxSamples, MaxTrackSamples: maxTrackSamples, MaxSampleBytes: maxSampleBytes, PosterWidth: posterW,
		}
		req := proto.Request{ID: 7, Op: op, Key: proto.Fingerprint(data), HasData: hasData, T: tm, Limits: ask}
		res, img := s.Handle(req, data)
		if res.ID != 7 {
			t.Fatalf("answer to id %d", res.ID)
		}
		if !res.OK() {
			if !known[res.Code] || res.Error == "" || img != nil {
				t.Fatalf("refusal %+v with %d image bytes", res, len(img))
			}
			return
		}
		eff := proto.Clamp(ask)
		if op == proto.OpPoster {
			pic, err := jpeg.Decode(bytes.NewReader(img))
			if err != nil || pic.Bounds().Dx() != res.Width || pic.Bounds().Dy() != res.Height {
				t.Fatalf("the poster is not the JPEG it says: %v", err)
			}
			if res.Width > eff.PosterWidth {
				t.Fatalf("a poster %d wide, over the clamped width %d", res.Width, eff.PosterWidth)
			}
		}
		// a frame is within the limits of THIS request, and so within the worker's
		if res.Width > eff.MaxWidth && op == proto.OpProbe || res.Height > eff.MaxHeight && op == proto.OpProbe ||
			res.Width > proto.DefaultLimits.MaxWidth || res.Height > proto.DefaultLimits.MaxHeight {
			t.Fatalf("a frame %dx%d over the bounds %dx%d", res.Width, res.Height, eff.MaxWidth, eff.MaxHeight)
		}
		if len(data) > eff.MaxBytes && hasData {
			t.Fatalf("a file of %d bytes accepted, over MaxBytes %d", len(data), eff.MaxBytes)
		}
	})
}
