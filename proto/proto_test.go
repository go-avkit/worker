// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package proto

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// fields lists a Limits' bounds by name, so that a check cannot forget one
// when a field is added: the count is asserted against the struct.
func fields(t testing.TB, l Limits) map[string]int {
	t.Helper()
	v, out := reflect.ValueOf(l), map[string]int{}
	for i := range v.NumField() {
		out[v.Type().Field(i).Name] = int(v.Field(i).Int())
	}
	if len(out) != 9 {
		t.Fatalf("Limits has %d fields; this test knows 9: extend Clamp and the tests", len(out))
	}
	return out
}

// within says every field of got is in 1..default, naming the first that is not.
func within(t testing.TB, ask, got Limits) {
	t.Helper()
	d, g, a := fields(t, DefaultLimits), fields(t, got), fields(t, ask)
	for name, max := range d {
		if g[name] < 1 || g[name] > max {
			t.Fatalf("ask %s=%d gave %d; want 1..%d", name, a[name], g[name], max)
		}
		if a[name] > 0 && a[name] <= max && g[name] != a[name] {
			t.Fatalf("ask %s=%d (a lowering) gave %d", name, a[name], g[name])
		}
	}
}

// A request can only LOWER limits: each field, one at a time, at the values
// that matter (zero, negative, just below, equal, just above, the integer's
// extremes), and all at once.
func TestClampOnlyLowers(t *testing.T) {
	d := fields(t, DefaultLimits)
	for name, max := range d {
		for _, ask := range []int{math.MinInt, -1, 0, 1, max - 1, max, max + 1, 2 * max, math.MaxInt32, math.MaxInt} {
			var l Limits
			reflect.ValueOf(&l).Elem().FieldByName(name).SetInt(int64(ask))
			got := Clamp(l)
			within(t, l, got)
			switch want := ask; {
			case ask <= 0 || ask > max:
				if g := fields(t, got)[name]; g != max {
					t.Errorf("%s=%d: %d, want the default %d", name, ask, g, max)
				}
			default:
				if g := fields(t, got)[name]; g != want {
					t.Errorf("%s=%d: %d, want it kept", name, ask, g)
				}
			}
		}
	}
	var huge Limits
	v := reflect.ValueOf(&huge).Elem()
	for i := range v.NumField() {
		v.Field(i).SetInt(math.MaxInt)
	}
	if got := Clamp(huge); got != DefaultLimits {
		t.Errorf("a request of the maximum asks %+v, got %+v", huge, got)
	}
	if got := Clamp(Limits{}); got != DefaultLimits {
		t.Errorf("no request: %+v", got)
	}
}

// The independent reviewer's probe: for each of the 9 fields, a request of
// MaxInt, 2^40, 0 and -1. The meaning is written down here and in Clamp: a
// value above the default (MaxInt, 2^40), zero and a negative are all "use the
// default" (never a refusal and never the value asked); only a value in
// 1..default is a lowering and is kept. The effective value never exceeds the
// default.
func TestClampReviewerTable(t *testing.T) {
	for name, max := range fields(t, DefaultLimits) {
		for label, ask := range map[string]int64{"MaxInt": math.MaxInt, "2^40": 1 << 40, "0": 0, "-1": -1} {
			if int64(int(ask)) != ask {
				continue // 2^40 does not exist as an int on a 32-bit target
			}
			var l Limits
			reflect.ValueOf(&l).Elem().FieldByName(name).SetInt(ask)
			got := fields(t, Clamp(l))
			if got[name] > max || got[name] != max {
				t.Errorf("%s=%s: effective %d, want the default %d (use the default)", name, label, got[name], max)
			}
			for other, v := range got {
				if other != name && v != fields(t, DefaultLimits)[other] {
					t.Errorf("%s=%s moved %s to %d", name, label, other, v)
				}
			}
		}
	}
}

// The wire names are those of go-slides' video.Limits (Go field names), so
// that its client works unchanged.
func TestWireCompatibility(t *testing.T) {
	b, err := json.Marshal(Request{ID: 7, Op: OpPoster, Key: "9-ab", HasData: true, Format: "mp4", T: 1.5, Limits: DefaultLimits})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":7,"op":"poster","key":"9-ab","hasData":true,"format":"mp4","t":1.5,"limits":{"MaxBytes":67108864,"MaxBoxes":262144,"MaxDepth":16,"MaxWidth":4096,"MaxHeight":4096,"MaxSamples":600,"MaxTrackSamples":262144,"MaxSampleBytes":8388608,"PosterWidth":1280}}`
	if string(b) != want {
		t.Errorf("request wire form\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(Response{ID: 7, Format: "mp4", MIME: "video/mp4", Codec: "avc1", Width: 2, Height: 3, Duration: 1.5, Decodable: true, Image: "image/jpeg"})
	const wantRes = `{"id":7,"format":"mp4","mime":"video/mp4","codec":"avc1","width":2,"height":3,"duration":1.5,"decodable":true,"image":"image/jpeg"}`
	if string(b) != wantRes {
		t.Errorf("response wire form\n got %s\nwant %s", b, wantRes)
	}
	b, _ = json.Marshal(Response{ID: 1, Code: CodeNotHeld, Error: "x"})
	if string(b) != `{"id":1,"code":"video.notheld","error":"x"}` || (Response{ID: 1, Code: CodeLimit}).OK() || !(Response{}).OK() {
		t.Errorf("refusal wire form %s", b)
	}
}

func TestDecodeRequest(t *testing.T) {
	req, err := DecodeRequest([]byte(` {"id":3,"op":"probe","key":"5-1f","future":true} `))
	if err != nil || req.ID != 3 || req.Op != OpProbe || req.Key != "5-1f" {
		t.Fatalf("a request with an unknown field: %+v, %v", req, err)
	}
	for name, tc := range map[string]struct {
		in   string
		code string
		id   int
	}{
		"not json":        {"hello", CodeSyntax, 0},
		"empty":           {"", CodeSyntax, 0},
		"wrong type":      {`{"id":9,"op":5}`, CodeSyntax, 9},
		"two values":      {`{"id":4} {"id":5}`, CodeSyntax, 4},
		"trailing junk":   {`{"id":4}x`, CodeSyntax, 4},
		"long key":        {`{"id":6,"key":"` + strings.Repeat("k", MaxKeyBytes+1) + `"}`, CodeSyntax, 6},
		"huge header":     {`{"id":8,"op":"` + strings.Repeat("o", MaxHeaderBytes) + `"}`, CodeLimit, 0},
		"id out of range": {`{"id":1e999}`, CodeSyntax, 0},
	} {
		req, err := DecodeRequest([]byte(tc.in))
		var e *Error
		if !errors.As(err, &e) || e.Code != tc.code || req.ID != tc.id {
			t.Errorf("%s: id %d, %v; want %s, id %d", name, req.ID, err, tc.code, tc.id)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a, b := Fingerprint([]byte("abc")), Fingerprint([]byte("abd"))
	if a == b || !strings.HasPrefix(a, "3-") || Fingerprint(nil) != "0-0" {
		t.Errorf("fingerprints %q %q %q", a, b, Fingerprint(nil))
	}
}

func TestErrorMessage(t *testing.T) {
	if (&Error{CodeLimit, "m"}).Error() != "m" {
		t.Error("Error() is the message")
	}
}
