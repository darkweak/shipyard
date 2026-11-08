package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// settings are instance-wide preferences. The defaults pre-fill the new
// project form; existing projects are not affected when they change.
type settings struct {
	DefaultEnv    map[string]string `json:"defaultEnv"`
	DefaultLabels map[string]string `json:"defaultLabels"`
}

const settingsKey = "settings"

func (s *Server) loadSettings(r *http.Request) (*settings, error) {
	out := &settings{}
	raw, ok, err := s.Store.GetMeta(r.Context(), settingsKey)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			return nil, fmt.Errorf("stored settings: %w", err)
		}
	}
	if out.DefaultEnv == nil {
		out.DefaultEnv = map[string]string{}
	}
	if out.DefaultLabels == nil {
		out.DefaultLabels = map[string]string{}
	}
	return out, nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadSettings(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in settings
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.DefaultEnv == nil {
		in.DefaultEnv = map[string]string{}
	}
	if in.DefaultLabels == nil {
		in.DefaultLabels = map[string]string{}
	}
	raw, _ := json.Marshal(in)
	if err := s.Store.SetMeta(r.Context(), settingsKey, string(raw)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// validate applies the rules projects use for env vars and labels.
func (in *settings) validate() error {
	for k, v := range in.DefaultEnv {
		if !envKey.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("environment variable %s: multi-line values are not supported", k)
		}
	}
	for k := range in.DefaultLabels {
		if k == "" || strings.ContainsAny(k, "= \t\n") {
			return fmt.Errorf("invalid label name %q", k)
		}
	}
	return nil
}
