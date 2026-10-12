package config

import "fmt"

// ResolveAstraDependencies returns a detached configuration. Targets and WS
// members are derived from account state at load time, so they are dropped
// here; WS sessions reuse qualified HTTP routes and therefore need the pool.
func ResolveAstraDependencies(value AstraRoutingSettings) (AstraRoutingSettings, error) {
	value = value.Configured()
	if value.CookiePool.RotateNodes {
		value.CookiePool.IPAffinity = true
	}
	value.SchedulingGroupIDs = append([]int64(nil), value.SchedulingGroupIDs...)
	value.CookiePool.SourceAccountIDs = append([]int64(nil), value.CookiePool.SourceAccountIDs...)
	if value.WSSession.Enabled {
		value.CookiePool.Enabled = true
	}
	if value.CookiePool.Enabled && len(value.CookiePool.SourceAccountIDs) == 0 {
		return value, fmt.Errorf("astra_source_required")
	}
	return value, value.Validate()
}
