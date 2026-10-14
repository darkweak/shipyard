package api

import (
	"net/http"
	"strings"

	"shipyard/internal/store"
	"shipyard/internal/templates"
)

type templateInput struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Dockerfile   string           `json:"dockerfile"`
	Dockerignore string           `json:"dockerignore"`
	Variables    []store.Variable `json:"variables"`
	DefaultPort  int              `json:"defaultPort"`
}

func (in templateInput) apply(t *store.Template) {
	t.Name = strings.TrimSpace(in.Name)
	t.Description = strings.TrimSpace(in.Description)
	t.Dockerfile = in.Dockerfile
	t.Dockerignore = in.Dockerignore
	t.Variables = in.Variables
	t.DefaultPort = in.DefaultPort
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTemplates(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateInput
	if !readJSON(w, r, &in) {
		return
	}
	t := &store.Template{}
	in.apply(t)
	if err := templates.Validate(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.CreateTemplate(r.Context(), t); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var in templateInput
	if !readJSON(w, r, &in) {
		return
	}
	in.apply(t)
	if err := templates.Validate(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.UpdateTemplate(r.Context(), t); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewTemplate renders either a stored template (templateId) or an unsaved
// draft (dockerfile + variables) with the given values.
func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TemplateID string            `json:"templateId"`
		Dockerfile string            `json:"dockerfile"`
		Variables  []store.Variable  `json:"variables"`
		Values     map[string]string `json:"values"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	t := &store.Template{Name: "preview", Dockerfile: in.Dockerfile, Variables: in.Variables}
	if in.TemplateID != "" {
		var err error
		if t, err = s.Store.GetTemplate(r.Context(), in.TemplateID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if err := templates.Validate(t); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	out, err := templates.Render(t, in.Values)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"dockerfile": out})
}
