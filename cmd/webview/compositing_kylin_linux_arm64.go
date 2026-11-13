//go:build linux && !android && arm64 && ofdgo_kylin

package main

import "os"

func init() {
	if _, set := os.LookupEnv("WEBKIT_DISABLE_COMPOSITING_MODE"); !set {
		os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
	}
}
