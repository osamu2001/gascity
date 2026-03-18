package runtime

// ProbeResult represents a bounded probe outcome for liveness checks.
// Distinguishes confirmed-alive, confirmed-dead, and unknown (timeout/error).
type ProbeResult int

const (
	// ProbeAlive means the process is confirmed alive.
	ProbeAlive ProbeResult = iota
	// ProbeDead means the process is confirmed dead or absent.
	ProbeDead
	// ProbeUnknown means liveness could not be determined (timeout or error).
	ProbeUnknown
)

// ProviderCapabilities describes what a runtime provider can report.
// Not all providers support all wake-reason inputs.
type ProviderCapabilities struct {
	// CanReportAttachment is true if IsAttached returns meaningful results.
	CanReportAttachment bool
	// CanReportActivity is true if GetLastActivity returns meaningful results.
	CanReportActivity bool
}
