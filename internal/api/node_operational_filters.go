package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"prism/internal/service"
)

const maxNodeLatencyMs = 3_600_000

func parseNodeLatencyBound(raw, field string) (*float64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !validNodeLatencyBound(value) {
		return nil, fmt.Errorf("%s: must be finite and between 0 and %d milliseconds", field, maxNodeLatencyMs)
	}
	return &value, nil
}

func validNodeLatencyBound(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= maxNodeLatencyMs
}

// List and export use the same bounds: minimum inclusive, maximum exclusive.
func validateNodeOperationalFilters(filters *service.NodeFilters) error {
	for _, bound := range []struct {
		name  string
		value *float64
	}{{"latency_min", filters.LatencyMin}, {"latency_max", filters.LatencyMax}} {
		if bound.value != nil && !validNodeLatencyBound(*bound.value) {
			return fmt.Errorf("%s: must be finite and between 0 and %d milliseconds", bound.name, maxNodeLatencyMs)
		}
	}
	if filters.LatencyMin != nil && filters.LatencyMax != nil && *filters.LatencyMin >= *filters.LatencyMax {
		return fmt.Errorf("latency_min: must be less than latency_max")
	}
	if filters.Region != nil {
		region := strings.ToLower(strings.TrimSpace(*filters.Region))
		if len(region) != 2 || region[0] < 'a' || region[0] > 'z' || region[1] < 'a' || region[1] > 'z' {
			return fmt.Errorf("region: must be a two-letter country/region code")
		}
		filters.Region = &region
	}
	return nil
}
