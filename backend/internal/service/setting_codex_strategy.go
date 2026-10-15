package service

import (
	"context"
	"strconv"
	"strings"
)

const SettingKeyOpenAICodexTicketStrategy = "openai_codex_ticket_strategy"
const SettingKeyOpenAICodexTicketStrict = "openai_codex_ticket_strict_response"

func (s *SettingService) CodexTicketStrictResponse(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexTicketStrict)
	return err == nil && value == "true"
}

func NormalizeCodexTicketStrategy(value string) string {
	if value == "fixed" {
		return "fixed"
	}
	return "standby"
}

// Read once per harvest round; missing settings retain early refresh.
func (s *SettingService) GetCodexTicketStrategy(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return "standby"
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexTicketStrategy)
	if err != nil {
		return "standby"
	}
	return NormalizeCodexTicketStrategy(value)
}

// Missing, malformed or unreadable settings leave the beta observer disabled.
const SettingKeyOpenAICodexTicketCandyObservation = "openai_codex_ticket_candy_observation"

func (s *SettingService) CodexTicketCandyObservation(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexTicketCandyObservation)
	return err == nil && value == "true"
}

// Missing or unreadable settings leave the gateway candy check disabled.
const SettingKeyOpenAICodexGatewayCandyCheck = "openai_codex_gateway_candy_check"

func (s *SettingService) CodexGatewayCandyCheck(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexGatewayCandyCheck)
	return err == nil && value == "true"
}

const SettingKeyOpenAICodexGatewayCandyPrefetch = "openai_codex_gateway_candy_prefetch"

func (s *SettingService) CodexGatewayCandyPrefetch(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexGatewayCandyPrefetch)
	return err == nil && value == "true"
}

const SettingKeyOpenAICodexGatewayCandyPrefer = "openai_codex_gateway_candy_prefer"

func (s *SettingService) CodexGatewayCandyPrefer(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexGatewayCandyPrefer)
	return err == nil && value == "true"
}

const SettingKeyOpenAICodexGatewayCandyConcurrency = "openai_codex_gateway_candy_concurrency"

func (s *SettingService) CodexGatewayCandyConcurrency(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return defaultGatewayCandyConcurrency
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexGatewayCandyConcurrency)
	if err != nil || strings.TrimSpace(value) == "" {
		return defaultGatewayCandyConcurrency
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return defaultGatewayCandyConcurrency
	}
	return NormalizeGatewayCandyConcurrency(parsed)
}

const (
	defaultGatewayCandyConcurrency = 3
	maxGatewayCandyConcurrency     = 32
)

func NormalizeGatewayCandyConcurrency(value int) int {
	if value < 1 {
		return defaultGatewayCandyConcurrency
	}
	if value > maxGatewayCandyConcurrency {
		return maxGatewayCandyConcurrency
	}
	return value
}
