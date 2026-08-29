package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/luispfcanales/rfelogappwasap/internal/whatsapp"
)

// Handlers agrupa las dependencias que los endpoints necesitan.
type Handlers struct {
	WA *whatsapp.Client
}

type sendMessageRequest struct {
	Number  string `json:"number"`
	Message string `json:"message"`
}

type sendDocumentRequest struct {
	Number   string `json:"number"`
	Filename string `json:"filename"`
	FileData string `json:"file_data"` // Base64 string
	MimeType string `json:"mimetype"`
	Caption  string `json:"caption"`
}

type apiResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

type qrResponse struct {
	Status  string `json:"status"`         // "ok" | "connected" | "timeout" | "error"
	Code    string `json:"code,omitempty"` // el código crudo, solo si status == "ok"
	Version int    `json:"version"`        // pásalo como ?since= en la siguiente llamada
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// POST /send  -> envía un mensaje de texto
func (h *Handlers) Send(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Status: "error", Error: "método no permitido"})
		return
	}

	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Status: "error", Error: "JSON inválido"})
		return
	}

	if req.Number == "" || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Status: "error", Error: "'number' y 'message' son requeridos"})
		return
	}

	if !h.WA.IsLoggedIn() {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Status: "error", Error: "WhatsApp no está conectado, usa GET /login"})
		return
	}

	msgID, err := h.WA.SendText(r.Context(), req.Number, req.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Status: "error", Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Status: "sent", Message: msgID})
}

// POST /send/document -> envía un documento PDF/archivo
func (h *Handlers) SendDocument(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Status: "error", Error: "método no permitido"})
		return
	}

	var req sendDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Status: "error", Error: "JSON inválido"})
		return
	}

	if req.Number == "" || req.FileData == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Status: "error", Error: "'number' y 'file_data' son requeridos"})
		return
	}

	if !h.WA.IsLoggedIn() {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Status: "error", Error: "WhatsApp no está conectado, usa GET /login"})
		return
	}

	pdfBytes, err := base64.StdEncoding.DecodeString(req.FileData)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Status: "error", Error: "Error decodificando Base64: " + err.Error()})
		return
	}

	msgID, err := h.WA.SendDocument(r.Context(), req.Number, req.Filename, pdfBytes, req.MimeType, req.Caption)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Status: "error", Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Status: "sent", Message: msgID})
}

// GET /login -> (re)inicia el flujo de login por QR en segundo plano
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.WA.StartLogin(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, apiResponse{Status: "error", Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Status: "started", Message: "sondea GET /login/qr?since=0 para obtener el código"})
}

// POST /logout -> desvincula la sesión de WhatsApp
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Status: "error", Error: "método no permitido"})
		return
	}

	if err := h.WA.Logout(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Status: "error", Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{Status: "disconnected", Message: "Sesión desvinculada exitosamente"})
}

// GET /login/qr?since=<version> -> long polling.
// Se queda esperando (hasta ~25s) a que el código cambie respecto a "since".
// El cliente debe volver a llamar pasando la "version" recibida como nuevo "since".
func (h *Handlers) QR(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	since := 0
	if s := r.URL.Query().Get("since"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			since = v
		}
	}

	if h.WA.IsLoggedIn() {
		writeJSON(w, http.StatusOK, qrResponse{Status: "connected"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	code, version := h.WA.WaitForQR(ctx, since)

	switch {
	case code != "":
		writeJSON(w, http.StatusOK, qrResponse{Status: "ok", Code: code, Version: version})
	case h.WA.IsLoggedIn():
		writeJSON(w, http.StatusOK, qrResponse{Status: "connected", Version: version})
	default:
		// se cumplió el timeout sin novedades; el cliente reintenta con el mismo since
		writeJSON(w, http.StatusOK, qrResponse{Status: "timeout", Version: version})
	}
}

// GET /status -> estado de la conexión
func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	status := "disconnected"
	if h.WA.IsLoggedIn() {
		status = "connected"
	}
	writeJSON(w, http.StatusOK, apiResponse{Status: status})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
