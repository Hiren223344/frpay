package api

import (
	"encoding/json"
	"io"
	"net/http"
)

const maxBodyBytes = 1 << 20 // 1MB

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func decodeJSON(r *http.Request, out interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(out)
}
