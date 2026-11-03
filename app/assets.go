package app

import (
	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"literature-translation/internal/render"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// AssetHandler serves embedded assets and only registered, local font files.
func AssetHandler(a *App) http.Handler {
	embedded := render.AssetHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			embedded.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/userfonts/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/userfonts/")
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `\/:`) {
			http.NotFound(w, r)
			return
		}
		c, err := config.Load()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		registered := false
		for _, f := range c.UserFonts {
			if f.FileName == name {
				registered = true
				break
			}
		}
		if !registered {
			http.NotFound(w, r)
			return
		}
		dir, err := fonts.Dir()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path, err := filepath.EvalSymlinks(filepath.Join(root, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(filepath.Dir(path), root) {
			http.NotFound(w, r)
			return
		}
		mime := ""
		switch strings.ToLower(filepath.Ext(name)) {
		case ".ttf":
			mime = "font/ttf"
		case ".otf":
			mime = "font/otf"
		default:
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}
