package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/paths"
	"seedstream/pkg/initialization"
)

// handleTrackerDefinitions serves the searchable catalog of tracker definitions
// behind the "add a tracker" picker: the user finds their tracker by name and
// only has to supply credentials, rather than knowing a Torznab URL.
//
// GET /api/trackers/definitions?q=<search>&limit=<n>
func (s *Server) handleTrackerDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cat := initialization.TrackerCatalog(paths.GetDataDir())

	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries := cat.Search(r.URL.Query().Get("q"), limit)

	writeJSON(w, map[string]any{
		"definitions":     entries,
		"total":           cat.Count(),
		"definitions_dir": cat.DefinitionsDir(),
	})
}

// handleTrackerDefinitionImport installs a definition file supplied by the
// operator, so the published Jackett/Prowlarr definition set can be added
// without rebuilding. The file is validated before it is stored, so a broken
// definition can never be written where it would be loaded at startup.
//
// POST /api/trackers/definitions/import   body: raw YAML, ?name=<filename>
func (s *Server) handleTrackerDefinitionImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "could not read definition", http.StatusBadRequest)
		return
	}
	cat := initialization.TrackerCatalog(paths.GetDataDir())
	def, err := cat.InstallDefinition(r.URL.Query().Get("name"), body)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	logger.Info("Tracker definition imported", "id", def.ID, "name", def.Name)
	writeJSON(w, map[string]any{
		"status": "success",
		"id":     def.ID,
		"name":   def.Name,
		"total":  cat.Count(),
	})
}

// handleTrackerDefinitionsReload re-reads the definitions directory, so a set
// dropped in on disk becomes available without restarting SeedStream.
//
// POST /api/trackers/definitions/reload
func (s *Server) handleTrackerDefinitionsReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cat := initialization.TrackerCatalog(paths.GetDataDir())
	loaded, failed := cat.Reload()
	writeJSON(w, map[string]any{
		"status":          "success",
		"loaded":          loaded,
		"rejected":        failed,
		"definitions_dir": cat.DefinitionsDir(),
	})
}

// writeJSONStatus writes a JSON body with an explicit status code, which the
// shared writeJSON helper does not support.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
