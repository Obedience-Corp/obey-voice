package netapi

import (
	"encoding/json"
	"io"
	"net/http"
)

// ConfigManagement is supplied by the host and enabled only for separately
// authorized named devices. Implementations own validation and persistence.
type ConfigManagement struct {
	Settings    func() (any, error)
	SetSetting  func(key, value string) (any, error)
	Persona     func(id string) (any, error)
	EditPersona func(id, field, value string) (any, error)
}

func (s *Server) requireConfigAccess(w http.ResponseWriter, r *http.Request) bool {
	_, allowed, err := s.opts.Credentials.requestConfigIdentity(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read permission"})
		return false
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this device does not have permission to edit host settings and personas"})
		return false
	}
	return true
}

func (s *Server) handleManagedSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigAccess(w, r) {
		return
	}
	result, err := s.opts.ConfigManagement.Settings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeManagedEdit(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	var body struct {
		Field string  `json:"field"`
		Value *string `json:"value"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Field == "" || body.Value == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a field and string value"})
		return "", "", false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected one JSON object"})
		return "", "", false
	}
	return body.Field, *body.Value, true
}

func (s *Server) handleManagedSettingEdit(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigAccess(w, r) {
		return
	}
	field, value, ok := decodeManagedEdit(w, r)
	if !ok {
		return
	}
	result, err := s.opts.ConfigManagement.SetSetting(field, value)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleManagedPersona(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigAccess(w, r) {
		return
	}
	result, err := s.opts.ConfigManagement.Persona(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleManagedPersonaEdit(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigAccess(w, r) {
		return
	}
	field, value, ok := decodeManagedEdit(w, r)
	if !ok {
		return
	}
	result, err := s.opts.ConfigManagement.EditPersona(r.PathValue("id"), field, value)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
