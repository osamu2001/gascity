package dashboard

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Serve starts the dashboard HTTP server. It creates an APIFetcher, builds
// the dashboard mux, and listens on the given port. This is the entry point
// called by the "gc dashboard serve" cobra command.
func Serve(port int, cityPath, cityName, apiURL string) error {
	log.Printf("dashboard: using API server at %s", apiURL)

	isSupervisor := detectSupervisor(apiURL)
	if isSupervisor {
		log.Printf("dashboard: supervisor mode detected, city selector enabled")
	}

	fetcher := NewAPIFetcher(apiURL, cityPath, cityName)

	mux, err := NewDashboardMux(
		fetcher,
		cityPath,
		cityName,
		apiURL,
		isSupervisor,
		8*time.Second,  // fetchTimeout
		30*time.Second, // defaultRunTimeout
		60*time.Second, // maxRunTimeout
	)
	if err != nil {
		return fmt.Errorf("dashboard: failed to create handler: %w", err)
	}

	addr := fmt.Sprintf(":%d", port)
	log.Printf("dashboard: listening on http://localhost%s", addr)
	return http.ListenAndServe(addr, mux)
}

// detectSupervisor probes the API server for supervisor mode by checking
// whether /v0/cities responds successfully.
func detectSupervisor(apiURL string) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(apiURL, "/") + "/v0/cities")
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var list struct {
		Items json.RawMessage `json:"items"`
	}
	return json.NewDecoder(resp.Body).Decode(&list) == nil && len(list.Items) > 0
}
