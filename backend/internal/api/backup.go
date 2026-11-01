package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
)

// createBackup answers with a backup of the server's data, encrypted with the passphrase
// in the body. It is written to a temporary file first, so a failure is still an error
// status rather than a cut download.
func (s *server) createBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeError(w, http.StatusServiceUnavailable, "backups are not available")
		return
	}
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	f, m, err := s.backups.Create(r.Context(), req.Passphrase)
	switch {
	case errors.Is(err, backup.ErrPassphrase):
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, backup.ErrBusy):
		s.writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.internalError(w, "create backup", err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		s.internalError(w, "create backup", err)
		return
	}
	name := backup.FileName(m.Created)
	auditTarget(r, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		s.log.Warn("send backup", "err", err)
	}
}
