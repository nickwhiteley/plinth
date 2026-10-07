package dataapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/nickwhiteley/plinth/code"
)

// Handler serves extraction over HTTP. The product mounts it behind its own authentication (an
// API key with an extraction permission, say), under any prefix, with http.StripPrefix:
//
//	GET  /extract                   the logs, with descriptions and stored cursors
//	GET  /extract/{table}           a page: ?after_txid=&after_log_id=&limit= (default 1000)
//	POST /extract/{table}/ack       store the cursor: {"txid": "…", "log_id": "…", "rows": n}
//	POST /extract/{table}/reset     forget the cursor
//
// Errors are JSON codes: {"code": "dataapi.unknown_table", "params": {…}}.
func Handler(e *Extractor, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		var ce *code.Error
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrUnknownTable):
			status = http.StatusNotFound
		case errors.Is(err, ErrCursorInvalid), errors.Is(err, ErrLimitInvalid):
			status = http.StatusBadRequest
		}
		if !errors.As(err, &ce) || status == http.StatusInternalServerError {
			if log != nil {
				log.Error("data extraction failed", "err", err)
			}
			ce = code.New("dataapi.failed")
		}
		writeJSON(w, status, ce)
	}
	mux.HandleFunc("GET /extract", func(w http.ResponseWriter, r *http.Request) {
		tables, err := e.Tables(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
	})
	mux.HandleFunc("GET /extract/{table}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		after, err := ParseCursor(q.Get("after_txid"), q.Get("after_log_id"))
		if err != nil {
			fail(w, err)
			return
		}
		limit := 1000
		if s := q.Get("limit"); s != "" {
			if limit, err = strconv.Atoi(s); err != nil {
				fail(w, ErrLimitInvalid)
				return
			}
		}
		p, err := e.Window(r.Context(), r.PathValue("table"), after, limit)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
	mux.HandleFunc("POST /extract/{table}/ack", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TxID  string `json:"txid"`
			LogID string `json:"log_id"`
			Rows  int    `json:"rows"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			fail(w, ErrCursorInvalid)
			return
		}
		c, err := ParseCursor(body.TxID, body.LogID)
		if err != nil {
			fail(w, err)
			return
		}
		if err := e.Ack(r.Context(), r.PathValue("table"), c, body.Rows); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /extract/{table}/reset", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Reset(r.Context(), r.PathValue("table")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
