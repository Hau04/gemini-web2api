package server

import (
	_ "embed"
	"net/http"
)

//go:embed ui/index.html
var accountManagerHTML []byte

func (a *App) handleAccountUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(accountManagerHTML)
}
