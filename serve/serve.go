// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Package serve answers proto requests with go-avkit: the worker's side of the
// protocol. It links go-avkit's container reader (MP4, WebM / Matroska) and
// go-avkit/decode's H.264 decoder, and nothing of a host: no syscall/js, so it
// is tested natively and run in a Web Worker by cmd/avkit-worker.
//
// A file is untrusted. Whatever the client said of it (its key, its size, its
// limits), it is checked against the clamped limits (proto.Clamp) before the
// container reader is asked anything, and again, against the limits of the
// request at hand, each time a held file is read.
package serve

import (
	"errors"
	"fmt"
	"sync"

	"github.com/go-avkit/worker/proto"
)

// Keep is how many files a Server holds: the project's videos that the
// renders of a page ask about, the most recent ones. Two 64 MiB files at most.
const Keep = 2

// Server holds the files it was sent and answers requests about them. Its
// methods are safe for concurrent use; they run one at a time.
type Server struct {
	mu    sync.Mutex
	files []held // most recent last
}

type held struct {
	key  string
	file *file
}

// Handle answers one request. payload is the file when req.HasData, and the
// picture of a poster's answer is the second result. The limits are
// proto.Clamp(req.Limits): a request lowers them, never raises them.
//
// A panic in the container reader or the decoder, on a file nobody foresaw, is
// answered as a video.syntax refusal and the worker keeps serving.
func (s *Server) Handle(req proto.Request, payload []byte) (res proto.Response, img []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			res = proto.Response{ID: req.ID, Code: proto.CodeSyntax, Error: fmt.Sprintf("the file could not be read (internal error: %v)", p)}
			img = nil
		}
	}()
	beforeHandle()
	return s.handle(req, payload)
}

// beforeHandle is a seam for the test of the recover: nothing else sets it.
var beforeHandle = func() {}

func (s *Server) handle(req proto.Request, payload []byte) (proto.Response, []byte) {
	res := proto.Response{ID: req.ID}
	lim := proto.Clamp(req.Limits)
	if req.Op != proto.OpPoster && req.Op != proto.OpProbe {
		res.Code, res.Error = proto.CodeSyntax, "unknown operation "+quote(req.Op)
		return res, nil
	}
	f, err := s.file(req, payload, lim)
	if err != nil {
		if errors.Is(err, errNotHeld) {
			res.Code, res.Error = proto.CodeNotHeld, "the worker does not hold this file"
			return res, nil
		}
		res.Code, res.Error = codeOf(err)
		return res, nil
	}
	res.Format, res.MIME, res.Codec = f.format, f.mime, f.codec
	res.Width, res.Height, res.Duration, res.Decodable = f.width, f.height, f.duration, f.decodable
	if req.Op == proto.OpProbe {
		return res, nil
	}
	jpg, w, h, err := f.poster(req.T, lim)
	if err != nil {
		res.Code, res.Error = codeOf(err)
		return res, nil
	}
	res.Width, res.Height, res.Image = w, h, "image/jpeg"
	return res, jpg
}

var errNotHeld = errors.New("not held")

// file is the checked file a request names: the payload, opened and kept, or
// the copy held under the key, checked again against this request's limits.
func (s *Server) file(req proto.Request, payload []byte, lim proto.Limits) (*file, error) {
	if !req.HasData {
		for _, h := range s.files {
			if h.key == req.Key {
				if _, _, err := sniff(h.file.data, lim); err != nil {
					return nil, err
				}
				return h.file, h.file.frameWithin(lim)
			}
		}
		return nil, errNotHeld
	}
	format, mime, err := sniff(payload, lim)
	if err != nil {
		return nil, err
	}
	f, err := open(payload, format, mime, lim)
	if err != nil {
		return nil, err
	}
	// a file is held under the key the client gave it: the worker does not
	// trust it to be the file's checksum, only to name what to look up
	s.files = append(without(s.files, req.Key), held{req.Key, f})
	if len(s.files) > Keep {
		s.files = s.files[len(s.files)-Keep:]
	}
	return f, nil
}

func without(h []held, key string) []held {
	out := make([]held, 0, len(h)+1)
	for _, x := range h {
		if x.key != key {
			out = append(out, x)
		}
	}
	return out
}

// codeOf is a refusal's code and message; an error with no code is
// video.syntax.
func codeOf(err error) (code, msg string) {
	var e *proto.Error
	if errors.As(err, &e) {
		return e.Code, e.Msg
	}
	return proto.CodeSyntax, err.Error()
}

func quote(s string) string {
	if len(s) > 32 {
		s = s[:32] + "..."
	}
	return `"` + s + `"`
}
