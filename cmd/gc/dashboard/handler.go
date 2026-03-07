package dashboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFiles embed.FS

// ConvoyFetcher defines the interface for fetching convoy data.
type ConvoyFetcher interface {
	FetchConvoys() ([]ConvoyRow, error)
	FetchMergeQueue() ([]MergeQueueRow, error)
	FetchWorkers() ([]WorkerRow, error)
	FetchMail() ([]MailRow, error)
	FetchRigs() ([]RigRow, error)
	FetchDogs() ([]DogRow, error)
	FetchEscalations() ([]EscalationRow, error)
	FetchHealth() (*HealthRow, error)
	FetchQueues() ([]QueueRow, error)
	FetchAssigned() ([]AssignedRow, error)
	FetchMayor() (*MayorStatus, error)
	FetchIssues() ([]IssueRow, error)
	FetchActivity() ([]ActivityRow, error)
}

// ConvoyHandler handles HTTP requests for the convoy dashboard.
type ConvoyHandler struct {
	fetcher      *APIFetcher
	template     *template.Template
	fetchTimeout time.Duration
	csrfToken    string
	isSupervisor bool   // true when connected to a supervisor API
	apiURL       string // supervisor API URL for city list fetches
}

// NewConvoyHandler creates a new convoy handler.
func NewConvoyHandler(fetcher *APIFetcher, isSupervisor bool, apiURL string, fetchTimeout time.Duration, csrfToken string) (*ConvoyHandler, error) {
	tmpl, err := LoadTemplates()
	if err != nil {
		return nil, err
	}

	return &ConvoyHandler{
		fetcher:      fetcher,
		template:     tmpl,
		fetchTimeout: fetchTimeout,
		csrfToken:    csrfToken,
		isSupervisor: isSupervisor,
		apiURL:       apiURL,
	}, nil
}

// ServeHTTP handles GET / requests and renders the convoy dashboard.
func (h *ConvoyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	expandPanel := r.URL.Query().Get("expand")

	// In supervisor mode, resolve city list and selected city.
	var cities []CityTab
	var selectedCity string
	fetcher := h.fetcher
	if h.isSupervisor {
		cities = fetchCityTabs(h.apiURL)
		selectedCity = r.URL.Query().Get("city")
		if selectedCity == "" {
			// Default to first running city.
			for _, c := range cities {
				if c.Running {
					selectedCity = c.Name
					break
				}
			}
		}
		if selectedCity != "" {
			fetcher = h.fetcher.WithScope(selectedCity)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.fetchTimeout)
	defer cancel()

	var (
		convoys     []ConvoyRow
		mergeQueue  []MergeQueueRow
		workers     []WorkerRow
		mail        []MailRow
		rigs        []RigRow
		dogs        []DogRow
		escalations []EscalationRow
		health      *HealthRow
		queues      []QueueRow
		assigned    []AssignedRow
		mayor       *MayorStatus
		issues      []IssueRow
		activity    []ActivityRow
		wg          sync.WaitGroup
	)

	wg.Add(13)

	go func() {
		defer wg.Done()
		var err error
		convoys, err = fetcher.FetchConvoys()
		if err != nil {
			log.Printf("dashboard: FetchConvoys failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mergeQueue, err = fetcher.FetchMergeQueue()
		if err != nil {
			log.Printf("dashboard: FetchMergeQueue failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		workers, err = fetcher.FetchWorkers()
		if err != nil {
			log.Printf("dashboard: FetchWorkers failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mail, err = fetcher.FetchMail()
		if err != nil {
			log.Printf("dashboard: FetchMail failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		rigs, err = fetcher.FetchRigs()
		if err != nil {
			log.Printf("dashboard: FetchRigs failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		dogs, err = fetcher.FetchDogs()
		if err != nil {
			log.Printf("dashboard: FetchDogs failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		escalations, err = fetcher.FetchEscalations()
		if err != nil {
			log.Printf("dashboard: FetchEscalations failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		health, err = fetcher.FetchHealth()
		if err != nil {
			log.Printf("dashboard: FetchHealth failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		queues, err = fetcher.FetchQueues()
		if err != nil {
			log.Printf("dashboard: FetchQueues failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		assigned, err = fetcher.FetchAssigned()
		if err != nil {
			log.Printf("dashboard: FetchAssigned failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mayor, err = fetcher.FetchMayor()
		if err != nil {
			log.Printf("dashboard: FetchMayor failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		issues, err = fetcher.FetchIssues()
		if err != nil {
			log.Printf("dashboard: FetchIssues failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		activity, err = fetcher.FetchActivity()
		if err != nil {
			log.Printf("dashboard: FetchActivity failed: %v", err)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("dashboard: fetch timeout after %v", h.fetchTimeout)
		<-done
	}

	summary := computeSummary(workers, assigned, issues, convoys, escalations, activity)

	data := ConvoyData{
		Convoys:      convoys,
		MergeQueue:   mergeQueue,
		Workers:      workers,
		Mail:         mail,
		Rigs:         rigs,
		Dogs:         dogs,
		Escalations:  escalations,
		Health:       health,
		Queues:       queues,
		Assigned:     assigned,
		Mayor:        mayor,
		Issues:       enrichIssuesWithAssignees(issues, assigned),
		Activity:     activity,
		Summary:      summary,
		Expand:       expandPanel,
		CSRFToken:    h.csrfToken,
		Cities:       cities,
		SelectedCity: selectedCity,
	}

	var buf bytes.Buffer
	if err := h.template.ExecuteTemplate(&buf, "convoy.html", data); err != nil {
		log.Printf("dashboard: template execution failed: %v", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("dashboard: response write failed: %v", err)
	}
}

// ServeActivityPanel handles GET /panels/activity for targeted panel refresh.
// Only fetches activity data (1 API call instead of 13), renders just the
// activity panel HTML fragment. Used by the JS event router for high-frequency
// observation events that don't affect other panels.
func (h *ConvoyHandler) ServeActivityPanel(w http.ResponseWriter, r *http.Request) {
	fetcher := h.fetcher
	if city := r.URL.Query().Get("city"); city != "" {
		fetcher = h.fetcher.WithScope(city)
	}
	activity, err := fetcher.FetchActivity()
	if err != nil {
		log.Printf("dashboard: FetchActivity failed: %v", err)
		// Return 503 so htmx skips the swap, preserving existing panel content.
		http.Error(w, "Activity data unavailable", http.StatusServiceUnavailable)
		return
	}

	data := ConvoyData{Activity: activity}

	var buf bytes.Buffer
	if err := h.template.ExecuteTemplate(&buf, "_panel_activity.html", data); err != nil {
		log.Printf("dashboard: activity panel template failed: %v", err)
		http.Error(w, "Failed to render panel", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("dashboard: activity panel write failed: %v", err)
	}
}

// computeSummary calculates dashboard stats and alerts from fetched data.
func computeSummary(workers []WorkerRow, assigned []AssignedRow, issues []IssueRow,
	convoys []ConvoyRow, escalations []EscalationRow, activity []ActivityRow,
) *Summary {
	summary := &Summary{
		PolecatCount:    len(workers),
		AssignedCount:   len(assigned),
		IssueCount:      len(issues),
		ConvoyCount:     len(convoys),
		EscalationCount: len(escalations),
	}

	for _, w := range workers {
		if w.WorkStatus == "stuck" {
			summary.StuckPolecats++
		}
	}
	for _, a := range assigned {
		if a.IsStale {
			summary.StaleAssigned++
		}
	}
	for _, e := range escalations {
		if !e.Acked {
			summary.UnackedEscalations++
		}
	}
	for _, i := range issues {
		if i.Priority == 1 || i.Priority == 2 {
			summary.HighPriorityIssues++
		}
	}
	for _, a := range activity {
		if a.Type == "session_death" || a.Type == "mass_death" {
			summary.DeadSessions++
		}
	}

	summary.HasAlerts = summary.StuckPolecats > 0 ||
		summary.StaleAssigned > 0 ||
		summary.UnackedEscalations > 0 ||
		summary.DeadSessions > 0 ||
		summary.HighPriorityIssues > 0

	return summary
}

// fetchCityTabs fetches the city list from the supervisor API for the city selector.
func fetchCityTabs(apiURL string) []CityTab {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(apiURL, "/") + "/v0/cities")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close() //nolint:errcheck
		}
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck

	var list struct {
		Items []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"items"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return nil
	}

	tabs := make([]CityTab, 0, len(list.Items))
	for _, c := range list.Items {
		tabs = append(tabs, CityTab{Name: c.Name, Running: c.Running})
	}
	return tabs
}

// enrichIssuesWithAssignees adds Assignee info to issues by cross-referencing assigned beads.
func enrichIssuesWithAssignees(issues []IssueRow, assigned []AssignedRow) []IssueRow {
	assigneeMap := make(map[string]string)
	for _, a := range assigned {
		assigneeMap[a.ID] = a.Agent
	}
	for i := range issues {
		if agent, ok := assigneeMap[issues[i].ID]; ok {
			issues[i].Assignee = agent
		}
	}
	return issues
}

// generateCSRFToken creates a cryptographically random token for CSRF protection.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate CSRF token: %v", err)
	}
	return hex.EncodeToString(b)
}

// NewDashboardMux creates an HTTP handler that serves both the dashboard and API.
func NewDashboardMux(fetcher *APIFetcher, cityPath, cityName, apiURL string, isSupervisor bool,
	fetchTimeout, defaultRunTimeout, maxRunTimeout time.Duration,
) (http.Handler, error) {
	csrfToken := generateCSRFToken()

	convoyHandler, err := NewConvoyHandler(fetcher, isSupervisor, apiURL, fetchTimeout, csrfToken)
	if err != nil {
		return nil, err
	}

	apiHandler := NewAPIHandler(cityPath, cityName, apiURL, "", defaultRunTimeout, maxRunTimeout, csrfToken)

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	staticHandler := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("/panels/activity", convoyHandler.ServeActivityPanel)
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler))
	mux.Handle("/", convoyHandler)

	return mux, nil
}
