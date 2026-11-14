// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

// Command avkit-worker is a video worker for the browser: go-avkit's container
// reader and H.264 decoder in a WebAssembly module of its own, run in a Web
// Worker the first time a page needs a poster or a probe. The page's own module
// links none of it.
//
// A host posts {videoPort: port} to the worker; the requests (package proto)
// arrive on that port as {json, data}: the request's JSON header and, in data,
// the file (an ArrayBuffer). The answer goes back the same way: {json, data},
// with the poster's JPEG in data. Nothing is fetched here. The worker posts
// {"ready":true} once it listens.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"syscall/js"

	"github.com/go-avkit/worker/proto"
	"github.com/go-avkit/worker/serve"
)

func main() {
	srv := &serve.Server{}
	js.Global().Set("onmessage", js.FuncOf(func(_ js.Value, args []js.Value) any {
		// a message of the wrong shape, or a port that is not one, is ignored
		defer func() { _ = recover() }()
		if len(args) == 0 || args[0].Type() != js.TypeObject {
			return nil
		}
		data := args[0].Get("data")
		if data.Type() != js.TypeObject {
			return nil
		}
		port := data.Get("videoPort")
		if port.Type() != js.TypeObject || port.Get("addEventListener").Type() != js.TypeFunction ||
			port.Get("postMessage").Type() != js.TypeFunction || port.Get("start").Type() != js.TypeFunction {
			return nil
		}
		port.Call("addEventListener", "message", js.FuncOf(func(_ js.Value, a []js.Value) any {
			if len(a) > 0 {
				answer(srv, port, a[0].Get("data"))
			}
			return nil
		}))
		port.Call("start")
		return nil
	}))
	js.Global().Call("postMessage", `{"ready":true}`)
	select {}
}

// answer reads one request off a port and posts its answer.
// A message of the wrong shape, or a panic, is answered with a refusal and the
// worker keeps serving.
func answer(srv *serve.Server, port, msg js.Value) {
	id := 0
	defer func() {
		if p := recover(); p != nil {
			post(port, proto.Response{ID: id, Code: proto.CodeSyntax, Error: fmt.Sprint("the request could not be served: ", p)}, nil)
		}
	}()
	if msg.Type() != js.TypeObject || msg.Get("json").Type() != js.TypeString {
		return
	}
	req, err := proto.DecodeRequest([]byte(msg.Get("json").String()))
	id = req.ID
	if err != nil {
		code, text := codeOf(err)
		post(port, proto.Response{ID: req.ID, Code: code, Error: text}, nil)
		return
	}
	var payload []byte
	if d := msg.Get("data"); !d.IsUndefined() && !d.IsNull() {
		// the file is bounded before it is copied into this module's memory
		if d.Type() != js.TypeObject || d.Get("byteLength").Type() != js.TypeNumber {
			post(port, proto.Response{ID: req.ID, Code: proto.CodeSyntax, Error: "the payload is not an ArrayBuffer"}, nil)
			return
		}
		n := d.Get("byteLength").Float()
		if n < 0 || n != math.Floor(n) || n > float64(proto.DefaultLimits.MaxBytes) {
			post(port, proto.Response{ID: req.ID, Code: proto.CodeLimit, Error: "the file is larger than the 64 MiB read"}, nil)
			return
		}
		payload = make([]byte, int(n))
		js.CopyBytesToGo(payload, js.Global().Get("Uint8Array").New(d))
	}
	res, img := srv.Handle(req, payload)
	post(port, res, img)
}

func codeOf(err error) (string, string) {
	if e, ok := err.(*proto.Error); ok {
		return e.Code, e.Msg
	}
	return proto.CodeSyntax, err.Error()
}

func post(port js.Value, res proto.Response, img []byte) {
	b, _ := json.Marshal(res)
	m := map[string]any{"json": string(b)}
	if len(img) > 0 {
		buf := js.Global().Get("Uint8Array").New(len(img))
		js.CopyBytesToJS(buf, img)
		m["data"] = buf.Get("buffer")
		port.Call("postMessage", m, []any{buf.Get("buffer")})
		return
	}
	port.Call("postMessage", m)
}
