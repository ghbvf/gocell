package cell

// HealthStatus reports the health of a Cell.
type HealthStatus struct {
	Status  string // "healthy" | "degraded" | "unhealthy"
	Details map[string]string
}
