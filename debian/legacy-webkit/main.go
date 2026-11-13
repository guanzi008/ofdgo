package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

func main() {
	if err := prepare(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare() error {
	assets := filepath.Join("assets", "webui")
	err := filepath.WalkDir(assets, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		extension := filepath.Ext(path)
		if extension != ".js" && extension != ".css" {
			return nil
		}
		input, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		loader := api.LoaderJS
		if extension == ".css" {
			loader = api.LoaderCSS
			input = []byte(strings.ReplaceAll(strings.ReplaceAll(string(input), ":is(", ":-webkit-any("), ":where(", ":-webkit-any("))
			input = regexp.MustCompile(`([0-9.])[dsl]v([hw])\b`).ReplaceAll(input, []byte(`${1}v${2}`))
			input = regexp.MustCompile(`(?m)^([ \t]*)mask:([^\n]+)$`).ReplaceAll(input, []byte("${1}-webkit-mask:${2}\n${1}mask:${2}"))
		}
		result := api.Transform(string(input), api.TransformOptions{
			Loader: loader, Sourcefile: path, Engines: []api.Engine{{Name: api.EngineSafari, Version: "13.1"}},
			Charset: api.CharsetUTF8, MinifyWhitespace: true, MinifySyntax: true, LegalComments: api.LegalCommentsInline,
		})
		if len(result.Errors) > 0 {
			return fmt.Errorf("%s: %s", path, result.Errors[0].Text)
		}
		return os.WriteFile(path, result.Code, 0644)
	})
	if err != nil {
		return err
	}
	for from, to := range map[string]string{"loader.js": "ofdgo_legacy_loader.js", "runtime.js": "ofdgo_legacy.js", "dialog.css": "ofdgo_legacy_dialog.css", "fallback.css": "ofdgo_legacy.css"} {
		data, err := os.ReadFile(filepath.Join("debian", "legacy-webkit", from))
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(assets, to), data, 0644); err != nil {
			return err
		}
	}
	embedPath := filepath.Join(assets, "assets.go")
	embed, err := os.ReadFile(embedPath)
	if err != nil {
		return err
	}
	embed = []byte(strings.ReplaceAll(string(embed), "//go:embed index.html ", "//go:embed ofdgo_legacy_loader.js ofdgo_legacy.js ofdgo_legacy_dialog.css ofdgo_legacy.css index.html "))
	if err = os.WriteFile(embedPath, embed, 0644); err != nil {
		return err
	}
	indexPath := filepath.Join(assets, "index.html")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	html := strings.Replace(string(index), "<script src=\"./ofdgo_theme.js\"></script>", "<script src=\"./ofdgo_legacy_loader.js\"></script>\n\t<script src=\"./ofdgo_theme.js\"></script>", 1)
	html = strings.Replace(html, "<link rel=\"stylesheet\" href=\"./ofdgo.css\">", "<link rel=\"stylesheet\" href=\"./ofdgo_legacy_dialog.css\">\n\t<link rel=\"stylesheet\" href=\"./ofdgo.css\">\n\t<link rel=\"stylesheet\" href=\"./ofdgo_legacy.css\">", 1)
	if !strings.Contains(html, "ofdgo_legacy_loader.js") {
		return fmt.Errorf("missing WebUI script entry")
	}
	return os.WriteFile(indexPath, []byte(html), 0644)
}
