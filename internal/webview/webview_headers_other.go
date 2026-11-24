//go:build !(linux && !android && cgo && (loong64 || (guigo_gtk3 && (amd64 || arm64))))

package webview

import "net/http"

func setGTK3Headers(http.Header, bool) {}
