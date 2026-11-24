//go:build !android && cgo && (loong64 || (guigo_gtk3 && (amd64 || arm64)))

package webview

import "net/http"

// GTK 3 没有 GTK 4 宿主的资源拦截规则，保留旧桌面端的同源策略。
func setGTK3Headers(header http.Header, documentResource bool) {
	if documentResource {
		header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		return
	}
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self' blob: data:; media-src 'self' blob: data:; connect-src 'self' blob: data:; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}
