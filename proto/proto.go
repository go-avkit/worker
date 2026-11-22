// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Package proto is the message protocol of a video worker: a program that
// reads video files for a page that must not link a container reader or a
// decoder (a WebAssembly module of its own, run in a Web Worker). It is two
// typed requests,
//
//	poster(file, t)  -> a JPEG, its width and height
//	probe(file)      -> container, codec, size, duration
//
// answered with coded refusals (video.limit, video.syntax, video.codec,
// video.unavailable). The messages are plain data: the transport is the host's
// (a MessagePort between two Web Workers in a browser; a function call in the
// tests). This package imports nothing but the standard library: no syscall/js,
// no decoder, so a client and a server can both use it.
//
// A message is a JSON header and, beside it, a binary payload: the file in a
// request, the picture in a response. The payload is never inside the JSON (a
// file is up to 64 MiB). A worker keeps the last files it was sent, by Key, so
// that the next requests for the same file (a poster at another time) do not
// carry it again; a request for a file it does not hold is answered CodeNotHeld,
// and the client sends it again with the file.
//
// The limits are the worker's: a request may LOWER any bound, never raise one
// (Clamp).
package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/go-simd/crc64"
)

// The operations.
const (
	OpPoster = "poster"
	OpProbe  = "probe"
)

// The codes of a refusal.
const (
	CodeLimit       = "video.limit"
	CodeSyntax      = "video.syntax"
	CodeCodec       = "video.codec"
	CodeUnavailable = "video.unavailable"
)

// CodeNotHeld is the code of an answer that is not a refusal of the file: the
// worker does not hold the file the request names. It is never shown.
const CodeNotHeld = "video.notheld"

// Error is a refusal with the code that names it.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// MaxHeaderBytes is the largest JSON header DecodeRequest reads. A request
// header is a few hundred bytes; the file is the payload, not the header.
const MaxHeaderBytes = 64 << 10

// MaxKeyBytes is the longest Key accepted (a length, a dash and 16 hex digits
// is under 32 bytes).
const MaxKeyBytes = 128

// Limits bound what a file may cost. The field names are the wire names: they
// are those of go-slides' video.Limits, which the clients encode as they are.
type Limits struct {
	// MaxBytes is the largest file read.
	MaxBytes int
	// MaxBoxes is the most boxes (MP4) or elements (EBML) a file may hold, and
	// MaxDepth how deeply they may nest.
	MaxBoxes, MaxDepth int
	// MaxWidth and MaxHeight bound the frame, in pixels.
	MaxWidth, MaxHeight int
	// MaxSamples is the most samples decoded for one poster (a keyframe far
	// from the time asked for is refused); MaxTrackSamples the most a track may
	// list; MaxSampleBytes the largest sample.
	MaxSamples, MaxTrackSamples, MaxSampleBytes int
	// PosterWidth is the width the poster is scaled down to (never up).
	PosterWidth int
}

// DefaultLimits are the worker's own bounds, which no request raises.
var DefaultLimits = Limits{
	MaxBytes: 64 << 20, MaxBoxes: 1 << 18, MaxDepth: 16,
	MaxWidth: 4096, MaxHeight: 4096,
	MaxSamples: 600, MaxTrackSamples: 1 << 18, MaxSampleBytes: 8 << 20,
	PosterWidth: 1280,
}

// Request asks a worker to read a file.
type Request struct {
	// ID pairs the answer with the request.
	ID int `json:"id"`
	// Op is OpPoster or OpProbe.
	Op string `json:"op"`
	// Key names the version of the file (Fingerprint).
	Key string `json:"key"`
	// HasData says the payload is the file; otherwise the worker's copy of the
	// file named by Key is read.
	HasData bool `json:"hasData,omitempty"`
	// Format is "mp4" or "webm", as the client sniffed it (informative: the
	// worker sniffs again).
	Format string `json:"format,omitempty"`
	// T is the poster's time, in seconds.
	T float64 `json:"t,omitempty"`
	// Limits are the bounds the host asks for; the worker never exceeds
	// DefaultLimits (Clamp).
	Limits Limits `json:"limits"`
}

// Response answers a Request. The poster's picture is the payload.
type Response struct {
	ID int `json:"id"`
	// Code and Error are a refusal: a video.* code and its message.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	// Probe's answer: the container ("mp4", "webm"), the video track, and
	// whether a poster can be decoded for it.
	Format    string  `json:"format,omitempty"`
	MIME      string  `json:"mime,omitempty"`
	Codec     string  `json:"codec,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	Duration  float64 `json:"duration,omitempty"`
	Decodable bool    `json:"decodable,omitempty"`
	// Image is the type of the payload of a poster's answer ("image/jpeg"); its
	// size is Width x Height for a poster.
	Image string `json:"image,omitempty"`
}

// OK reports whether the answer is a refusal's or not.
func (r Response) OK() bool { return r.Code == "" }

// DecodeRequest reads a request header. The header is bounded
// (MaxHeaderBytes), must be exactly one JSON object, and its Key is bounded
// (MaxKeyBytes). On an error the Request still carries the ID when one could
// be read, so that the worker can answer the refusal to the right request.
func DecodeRequest(b []byte) (Request, error) {
	var req Request
	if len(b) > MaxHeaderBytes {
		return req, &Error{CodeLimit, "the request header is larger than " + strconv.Itoa(MaxHeaderBytes) + " bytes"}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&req); err != nil {
		var id struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(b, &id) == nil {
			req = Request{ID: id.ID}
		} else {
			req = Request{}
		}
		return req, &Error{CodeSyntax, "the request header is not a request: " + err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Request{ID: req.ID}, &Error{CodeSyntax, "the request header holds more than one value"}
	}
	if len(req.Key) > MaxKeyBytes {
		return Request{ID: req.ID}, &Error{CodeSyntax, "the key is longer than " + strconv.Itoa(MaxKeyBytes) + " bytes"}
	}
	return req, nil
}

// Fingerprint names a version of a file: its length and a 64-bit checksum of
// every byte (CRC-64/ECMA), which costs a few milliseconds per 10 MiB and which
// a trimmed or re-encoded file does not keep.
func Fingerprint(data []byte) string {
	return strconv.Itoa(len(data)) + "-" + strconv.FormatUint(crc64.Checksum(data, crcTable), 16)
}

var crcTable = crc64.MakeTable(crc64.ECMA)

// Clamp is the limits a worker applies for a request's: each bound of ask is
// kept when it is positive and not above the worker's own (DefaultLimits), and
// replaced by it otherwise. A host can lower the bounds, never raise them:
// for every ask, every field of the result is in 1..DefaultLimits.
func Clamp(ask Limits) Limits {
	d := DefaultLimits
	c := func(a, max int) int {
		if a <= 0 || a > max {
			return max
		}
		return a
	}
	return Limits{
		MaxBytes: c(ask.MaxBytes, d.MaxBytes), MaxBoxes: c(ask.MaxBoxes, d.MaxBoxes), MaxDepth: c(ask.MaxDepth, d.MaxDepth),
		MaxWidth: c(ask.MaxWidth, d.MaxWidth), MaxHeight: c(ask.MaxHeight, d.MaxHeight),
		MaxSamples: c(ask.MaxSamples, d.MaxSamples), MaxTrackSamples: c(ask.MaxTrackSamples, d.MaxTrackSamples),
		MaxSampleBytes: c(ask.MaxSampleBytes, d.MaxSampleBytes), PosterWidth: c(ask.PosterWidth, d.PosterWidth),
	}
}
