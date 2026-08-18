// Package webpublic serves the public gallery: a bare grid of daily best
// frames, newest first, with a lightbox. Nothing else is visible.
package webpublic

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/davidtorcivia/westward/internal/store"
)

//go:embed templates/*.html static/*
var content embed.FS

type Gallery struct {
	Store    *store.Store
	DataRoot string
	tpl      *template.Template
	// attribution lookup: camera id -> string
	attr func(cameraID string) string
}

func New(st *store.Store, dataRoot string, attr func(string) string) (*Gallery, error) {
	g := &Gallery{Store: st, DataRoot: dataRoot, attr: attr}
	t, err := template.ParseFS(content, "templates/grid.html")
	if err != nil {
		return nil, err
	}
	g.tpl = t
	return g, nil
}

type cell struct {
	Date   string
	Score  float64
	HasImg bool
	// URL pieces derived from stored paths.
	Best, T480, T240 string
	Alt              string
	Attr             string
}

var hashFile = regexp.MustCompile(`^(thumb/)?(\d{4}-\d{2}-\d{2})\.([0-9A-Za-z]{8})\.jpg$`)
var thumbFile = regexp.MustCompile(`^thumb/(\d{4}-\d{2}-\d{2})\.([0-9A-Za-z]{8})\.(480|240)\.jpg$`)

// toURL maps a stored /data/best/... path to its public /img/ URL.
func toURL(path, dataRoot string) string {
	rel, err := filepath.Rel(filepath.Join(dataRoot, "best"), path)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if thumbFile.MatchString(rel) {
		return "/img/best/" + rel
	}
	if hashFile.MatchString(rel) {
		return "/img/best/" + rel
	}
	return ""
}

func (g *Gallery) Register(mux *http.ServeMux) {
	mux.Handle("GET /{$}", http.HandlerFunc(g.grid))
	mux.Handle("GET /older", http.HandlerFunc(g.grid)) // ?page=N
	mux.Handle("GET /img/best/{file...}", http.HandlerFunc(g.image))
}

func (g *Gallery) grid(w http.ResponseWriter, r *http.Request) {
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 10000 {
			page = n
		}
	}
	const perPage = 120
	days, err := g.Store.LatestDays(perPage, (page-1)*perPage)
	if err != nil {
		http.Error(w, "gallery unavailable", http.StatusInternalServerError)
		return
	}
	cells := make([]cell, 0, len(days))
	for _, d := range days {
		c := cell{Date: d.Date}
		if d.Status == "complete" && d.BestPath != "" {
			c.HasImg = true
			c.Score = 0
			if d.BestScore != nil {
				c.Score = *d.BestScore
			}
			c.Best = toURL(d.BestPath, g.DataRoot)
			c.T480 = toURL(d.Thumb480Path, g.DataRoot)
			c.T240 = toURL(d.Thumb240Path, g.DataRoot)
			c.Alt = fmt.Sprintf("Sunset %s, score %.1f", d.Date, c.Score)
			if d.BestCameraID != "" && g.attr != nil {
				c.Attr = g.attr(d.BestCameraID)
			}
		}
		cells = append(cells, c)
	}
	w.Header().Set("Cache-Control", "max-age=300")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	g.tpl.Execute(w, map[string]any{
		"Cells": cells, "Page": page, "HasOlder": len(cells) == perPage, "NextPage": page + 1,
	})
}

func (g *Gallery) image(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !hashFile.MatchString(file) && !thumbFile.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(g.DataRoot, "best", filepath.FromSlash(file))
	// Defense in depth: the path must resolve inside /best.
	if filepath.Clean(full) != filepath.Join(g.DataRoot, "best", file) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, file, st.ModTime(), f)
}

// Static serves the gallery's embedded CSS/JS under /static/.
func Static() http.Handler {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
