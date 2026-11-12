package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

const excelBPSModelPermissionCooldown = 30 * time.Minute
const excelBPSModelPermissionFilterReason = "excel_bps_model_not_allowed"
const excelBPSModelPermissionMaxEntries = 4096

type excelBPSModelKey struct {
	accountID int64
	model     string
}
type excelBPSModelCooldowns struct {
	mu    sync.Mutex
	until map[excelBPSModelKey]time.Time
}

func isExcelBPSModelPermissionError(status int, code string) bool {
	return status == 403 && (code == "basispoints_model_access_changed" || code == "model_not_allowed")
}

// Store the actual upstream model, so aliases share a cooldown while native
// Codex requests and the account's other BPS models remain eligible.
func (s *OpenAIGatewayService) coolDownExcelBPSModel(account *Account, upstreamModel string) {
	if s == nil || account == nil || strings.TrimSpace(upstreamModel) == "" {
		return
	}
	if account.AuthConnectionID != "" && s.excelOAuthReauth != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.excelOAuthReauth.FailAuthConnection(ctx, account, 403)
		return
	}
	s.excelBPSModelCooldowns.extend(excelBPSModelKey{account.ID, upstreamModel}, time.Now().Add(excelBPSModelPermissionCooldown))
}

func (s *OpenAIGatewayService) isExcelBPSModelCoolingDown(account *Account, requestedModel string) bool {
	if s == nil || account == nil || !account.IsExcelBPSEnabledForModel(requestedModel) {
		return false
	}
	return s.excelBPSModelCooldowns.active(excelBPSModelKey{account.ID, account.GetMappedModel(requestedModel)})
}

// extend reports whether until replaced the key's expiry; a shorter cooldown
// never replaces a longer one. A full table evicts the soonest expiry.
func (c *excelBPSModelCooldowns) extend(key excelBPSModelKey, until time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.until == nil {
		c.until = make(map[excelBPSModelKey]time.Time)
	}
	for candidate, expiry := range c.until {
		if !now.Before(expiry) {
			delete(c.until, candidate)
		}
	}
	current, exists := c.until[key]
	if exists && !until.After(current) {
		return false
	}
	if !exists && len(c.until) >= excelBPSModelPermissionMaxEntries {
		var oldest excelBPSModelKey
		var expiry time.Time
		for candidate, candidateUntil := range c.until {
			if expiry.IsZero() || candidateUntil.Before(expiry) {
				oldest, expiry = candidate, candidateUntil
			}
		}
		delete(c.until, oldest)
	}
	c.until[key] = until
	return true
}

func (c *excelBPSModelCooldowns) active(key excelBPSModelKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, exists := c.until[key]
	if exists && time.Now().Before(until) {
		return true
	}
	if exists {
		delete(c.until, key)
	}
	return false
}
