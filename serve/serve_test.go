// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/go-avkit/worker/proto"
)

func read(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func send(s *Server, op string, data []byte, tm float64, lim proto.Limits) (proto.Response, []byte) {
	return s.Handle(proto.Request{ID: 1, Op: op, Key: proto.Fingerprint(data), HasData: true, T: tm, Limits: lim}, data)
}

func TestProbeAndPoster(t *testing.T) {
	for _, name := range []string{"bframes.mp4", "bframes.mkv"} {
		data := read(t, name)
		s := &Server{}
		res, img := send(s, proto.OpProbe, data, 0, proto.Limits{})
		if !res.OK() || img != nil || !res.Decodable || res.Codec != "avc1" || res.Width <= 0 || res.Duration <= 0 || res.ID != 1 {
			t.Fatalf("%s probe: %+v", name, res)
		}
		for _, at := range []float64{0, 0.3, 1000} { // 1000: past the end, the last picture
			res, img := send(s, proto.OpPoster, data, at, proto.Limits{})
			if !res.OK() || res.Image != "image/jpeg" {
				t.Fatalf("%s poster at %g: %+v", name, at, res)
			}
			pic, err := jpeg.Decode(bytes.NewReader(img))
			if err != nil || pic.Bounds().Dx() != res.Width || pic.Bounds().Dy() != res.Height {
				t.Fatalf("%s at %g: not the JPEG it says (%v)", name, at, err)
			}
		}
	}
}

func TestPosterWidthIsLoweredNotRaised(t *testing.T) {
	data := read(t, "bframes.mp4")
	res, _ := send(&Server{}, proto.OpPoster, data, 0, proto.Limits{})
	if res.Width > proto.DefaultLimits.PosterWidth {
		t.Fatalf("default poster is %d wide", res.Width)
	}
	low, _ := send(&Server{}, proto.OpPoster, data, 0, proto.Limits{PosterWidth: res.Width / 2})
	if !low.OK() || low.Width > res.Width/2 {
		t.Errorf("a lowered PosterWidth: %+v from %+v", low, res)
	}
	up, _ := send(&Server{}, proto.OpPoster, data, 0, proto.Limits{PosterWidth: 1 << 30})
	if up.Width != res.Width {
		t.Errorf("a raised PosterWidth changed the answer: %d, want %d", up.Width, res.Width)
	}
}

func TestOtherCodecsHaveNoPoster(t *testing.T) {
	s := &Server{}
	data := read(t, "hvc1.mp4")
	if res, _ := send(s, proto.OpProbe, data, 0, proto.Limits{}); !res.OK() || res.Decodable {
		t.Fatalf("probe: %+v", res)
	}
	if res, img := send(s, proto.OpPoster, data, 0, proto.Limits{}); res.Code != proto.CodeCodec || img != nil {
		t.Errorf("poster: %+v", res)
	}
}

func TestNotHeldThenResent(t *testing.T) {
	s := &Server{}
	data := read(t, "bframes.mp4")
	key := proto.Fingerprint(data)
	ask := proto.Request{ID: 9, Op: proto.OpPoster, Key: key}
	if res, _ := s.Handle(ask, nil); res.Code != proto.CodeNotHeld || res.ID != 9 {
		t.Fatalf("before: %+v", res)
	}
	if res, _ := s.Handle(proto.Request{ID: 10, Op: proto.OpProbe, Key: key, HasData: true}, data); !res.OK() {
		t.Fatalf("send: %+v", res)
	}
	if res, img := s.Handle(ask, nil); !res.OK() || len(img) == 0 {
		t.Fatalf("held: %+v", res)
	}
}

// The worker keeps the last Keep files; the oldest is let go, a resent file is
// moved to the front, not held twice.
func TestKeepsTheLastTwo(t *testing.T) {
	s := &Server{}
	a, b, c := read(t, "bframes.mp4"), read(t, "bframes.mkv"), read(t, "hvc1.mp4")
	for _, d := range [][]byte{a, b, a, c} { // a is refreshed, so b is the oldest when c comes
		if res, _ := send(s, proto.OpProbe, d, 0, proto.Limits{}); !res.OK() {
			t.Fatal(res)
		}
	}
	held := func(d []byte) bool {
		res, _ := s.Handle(proto.Request{Op: proto.OpProbe, Key: proto.Fingerprint(d)}, nil)
		return res.OK()
	}
	if !held(a) || held(b) || !held(c) || len(s.files) != Keep {
		t.Errorf("a %v b %v c %v, %d files", held(a), held(b), held(c), len(s.files))
	}
}

func TestHeldFileIsCheckedAgainstTheLimitsOfEachRequest(t *testing.T) {
	s := &Server{}
	data := read(t, "bframes.mp4")
	send(s, proto.OpProbe, data, 0, proto.Limits{})
	key := proto.Fingerprint(data)
	for name, lim := range map[string]proto.Limits{
		"bytes": {MaxBytes: 100}, "boxes": {MaxBoxes: 3}, "width": {MaxWidth: 8}, "height": {MaxHeight: 8},
	} {
		if res, _ := s.Handle(proto.Request{Op: proto.OpProbe, Key: key, Limits: lim}, nil); res.Code != proto.CodeLimit {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestRefusals(t *testing.T) {
	mp4 := read(t, "bframes.mp4")
	for name, tc := range map[string]struct {
		data []byte
		lim  proto.Limits
		want string
	}{
		"not a video":   {[]byte("hello, this is text"), proto.Limits{}, proto.CodeSyntax},
		"empty":         {nil, proto.Limits{}, proto.CodeSyntax},
		"too big":       {mp4, proto.Limits{MaxBytes: 100}, proto.CodeLimit},
		"too many":      {mp4, proto.Limits{MaxBoxes: 3}, proto.CodeLimit},
		"too deep":      {mp4, proto.Limits{MaxDepth: 1}, proto.CodeLimit},
		"too wide":      {mp4, proto.Limits{MaxWidth: 8}, proto.CodeLimit},
		"too high":      {mp4, proto.Limits{MaxHeight: 8}, proto.CodeLimit},
		"truncated mp4": {mp4[:40], proto.Limits{}, proto.CodeSyntax},
		"no video":      {box("ftyp", []byte("isom\x00\x00\x02\x00isom")), proto.Limits{}, proto.CodeSyntax},
	} {
		if res, _ := send(&Server{}, proto.OpProbe, tc.data, 0, tc.lim); res.Code != tc.want || res.Error == "" {
			t.Errorf("%s: %+v, want %s", name, res, tc.want)
		}
	}
	if res, _ := (&Server{}).Handle(proto.Request{ID: 4, Op: "format", Key: "x"}, nil); res.Code != proto.CodeSyntax || res.ID != 4 {
		t.Errorf("unknown operation: %+v", res)
	}
	long := strings.Repeat("x", 100)
	if res, _ := (&Server{}).Handle(proto.Request{Op: long}, nil); !strings.Contains(res.Error, "...") {
		t.Errorf("a long operation is quoted whole: %q", res.Error)
	}
	if c, m := codeOf(os.ErrNotExist); c != proto.CodeSyntax || m == "" {
		t.Errorf("an error with no code: %q %q", c, m)
	}
}

// Each code a worker answers is produced by a minimal input. video.unavailable
// is the one a Server never produces: it always has its reader and decoder, so
// the code is the client's, for a worker it could not reach; a test asserts
// that no input here yields it.
func TestEveryCodeHasAMinimalInput(t *testing.T) {
	s := &Server{}
	mp4, hvc := read(t, "bframes.mp4"), read(t, "hvc1.mp4")
	got := map[string]proto.Response{}
	got[proto.CodeLimit], _ = send(s, proto.OpProbe, mp4, 0, proto.Limits{MaxBytes: 1})
	got[proto.CodeSyntax], _ = send(s, proto.OpProbe, []byte("x"), 0, proto.Limits{})
	got[proto.CodeCodec], _ = send(s, proto.OpPoster, hvc, 0, proto.Limits{})
	got[proto.CodeNotHeld], _ = s.Handle(proto.Request{Op: proto.OpProbe, Key: "nothing"}, nil)
	for code, res := range got {
		if res.Code != code || res.Error == "" {
			t.Errorf("%s: %+v", code, res)
		}
	}
	if res, _ := send(s, proto.OpProbe, mp4, 0, proto.Limits{}); !res.OK() {
		t.Errorf("control: %+v", res)
	}
	for _, res := range got {
		if res.Code == proto.CodeUnavailable {
			t.Errorf("a Server answered %s", res.Code)
		}
	}
}

func TestPosterTimes(t *testing.T) {
	data := read(t, "bframes.mp4")
	s := &Server{}
	for _, at := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if res, _ := send(s, proto.OpPoster, data, at, proto.Limits{}); res.Code != proto.CodeSyntax {
			t.Errorf("t=%g: %+v", at, res)
		}
	}
}

func TestPosterWorkIsBounded(t *testing.T) {
	data := read(t, "bframes.mp4")
	var refused bool
	for _, at := range []float64{0.4, 0.8, 1.2, 2} {
		if res, _ := send(&Server{}, proto.OpPoster, data, at, proto.Limits{MaxSamples: 1}); res.Code == proto.CodeLimit {
			refused = true
		}
	}
	if !refused {
		t.Error("a poster far from its keyframe is not refused at MaxSamples=1")
	}
}

func box(typ string, body []byte) []byte {
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

var ftyp = box("ftyp", []byte("isom\x00\x00\x02\x00isom"))

// bomb runs one hostile request under a memory limit and asserts that it is
// REFUSED with a code, and that the worker did not build anything near the
// size of what the file claimed: the allocations of the call, beyond the
// payload that was handed in, stay under budget.
func bomb(t *testing.T, name string, data []byte, lim proto.Limits, want string, budget uint64) {
	t.Helper()
	old := debug.SetMemoryLimit(512 << 20)
	defer debug.SetMemoryLimit(old)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, img := send(&Server{}, proto.OpPoster, data, 0, lim)
	runtime.ReadMemStats(&after)
	if res.Code != want || img != nil {
		t.Fatalf("%s: %+v, want a %s refusal", name, res, want)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > budget {
		t.Errorf("%s: refusing cost %d bytes of allocation (budget %d)", name, got, budget)
	}
}

func TestBombsAreRefusedWithinMemory(t *testing.T) {
	many := append([]byte{}, ftyp...)
	for range 300000 {
		many = append(many, box("free", nil)...)
	}
	deep := box("free", nil)
	for range 40 {
		deep = box("trak", box("moov", deep))
	}
	deep = append(append([]byte{}, ftyp...), deep...)
	ebml := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80, 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	for range 300000 {
		ebml = append(ebml, 0xEC, 0x80)
	}
	ebmlDeep := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}
	for range 40 {
		ebmlDeep = append(ebmlDeep, 0x1F, 0x43, 0xB6, 0x75, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	}
	// a file one byte over the largest the worker reads: refused on its length
	// alone, whatever the request asked for
	over := make([]byte, proto.DefaultLimits.MaxBytes+1)
	copy(over, ftyp)
	huge := proto.Limits{MaxBytes: math.MaxInt, MaxBoxes: math.MaxInt, MaxDepth: math.MaxInt}

	bomb(t, "300000 boxes", many, proto.Limits{}, proto.CodeLimit, 1<<20)
	bomb(t, "boxes, a request asking for more", many, huge, proto.CodeLimit, 1<<20)
	bomb(t, "deep boxes", deep, huge, proto.CodeLimit, 1<<20)
	bomb(t, "300000 elements", ebml, huge, proto.CodeLimit, 1<<20)
	bomb(t, "deep elements", ebmlDeep, huge, proto.CodeLimit, 1<<20)
	bomb(t, "over the file size", over, huge, proto.CodeLimit, 1<<20)
}

// A held file is not a way round the bounds: nothing is held for a refused file.
func TestRefusedFileIsNotHeld(t *testing.T) {
	s := &Server{}
	send(s, proto.OpProbe, []byte("not a video"), 0, proto.Limits{})
	if len(s.files) != 0 {
		t.Errorf("%d files held", len(s.files))
	}
}
