package api

import (
	"net/http"

	"github.com/luispfcanales/rfelogappwasap/internal/whatsapp"
)

// NewRouter arma el mux con todos los endpoints de la API.
func NewRouter(waClient *whatsapp.Client) *http.ServeMux {
	h := &Handlers{WA: waClient}

	mux := http.NewServeMux()
	mux.HandleFunc("/send", h.Send)
	mux.HandleFunc("/send/document", h.SendDocument)
	mux.HandleFunc("/login", h.Login)
	mux.HandleFunc("/login/qr", h.QR)
	mux.HandleFunc("/logout", h.Logout)
	mux.HandleFunc("/status", h.Status)

	return mux
}
