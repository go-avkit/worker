// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build !(js && wasm)

// Command avkit-worker is a video worker for the browser and only builds for
// GOOS=js GOARCH=wasm; this file stands in on every other platform so that
// `go build ./...` and `go vet ./...` see a package.
package main

import (
	"fmt"
	"os"
)

var osExit = os.Exit

func main() {
	fmt.Fprintln(os.Stderr, "avkit-worker runs in a browser: build it with GOOS=js GOARCH=wasm")
	osExit(2)
}
