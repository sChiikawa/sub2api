package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const astraRoutingSettingKey = "astra_routing_experiment_v1"

// GetAstraRouting returns the saved configuration without derived members.
func (s *SettingService) GetAstraRouting(ctx context.Context) (config.AstraRoutingSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, astraRoutingSettingKey)
	if errors.Is(err, ErrSettingNotFound) || (err == nil && value == "") {
		if s.cfg == nil {
			return config.AstraRoutingSettings{}, nil
		}
		return config.AstraRoutingSettings{CookiePool: s.cfg.Gateway.CodexGatewayPin, WSSession: s.cfg.Gateway.CodexWSAnchor}.Configured(), nil
	}
	if err != nil {
		return config.AstraRoutingSettings{}, err
	}
	var result config.AstraRoutingSettings
	if err = json.Unmarshal([]byte(value), &result); err != nil {
		return result, fmt.Errorf("invalid Astra routing settings")
	}
	result = result.Configured()
	return result, result.Validate()
}

// GetAstraRoutingWithMembers returns the saved configuration together with the
// targets and WS members it currently resolves to.
func (s *SettingService) GetAstraRoutingWithMembers(ctx context.Context) (config.AstraRoutingSettings, error) {
	value, err := s.GetAstraRouting(ctx)
	if err != nil {
		return value, err
	}
	s.astraRoutingMu.Lock()
	defer s.astraRoutingMu.Unlock()
	return s.withAstraMembersLocked(ctx, value), nil
}

// SetAstraRouting prepares account dependencies atomically before publishing
// the new configuration. A failed save never starts model requests.
func (s *SettingService) SetAstraRouting(ctx context.Context, value config.AstraRoutingSettings) (config.AstraRoutingSettings, error) {
	var err error
	value, err = config.ResolveAstraDependencies(value)
	if err != nil {
		return value, err
	}
	if value.WSSession.Enabled && s.cfg != nil {
		ws := s.cfg.Gateway.OpenAIWS
		if ws.ForceHTTP || !ws.Enabled || !ws.OAuthEnabled || !ws.ResponsesWebsocketsV2 {
			return value, fmt.Errorf("astra_global_ws_disabled")
		}
	}
	s.astraRoutingMu.Lock()
	previous, readErr := s.GetAstraRouting(ctx)
	if readErr != nil {
		s.astraRoutingMu.Unlock()
		return value, readErr
	}
	schedulingOnly := config.AstraRouteSettingsEqual(previous, value)
	value.Revision = uuid.NewString()
	if schedulingOnly {
		value.Revision = previous.Revision
	}
	if writer, ok := s.settingRepo.(interface {
		SetAstraRoutingWithAccounts(context.Context, string, config.AstraRoutingSettings) error
	}); ok {
		err = writer.SetAstraRoutingWithAccounts(ctx, astraRoutingSettingKey, value)
	} else {
		var raw []byte
		raw, err = json.Marshal(value)
		if err == nil {
			err = s.settingRepo.Set(ctx, astraRoutingSettingKey, string(raw))
		}
	}
	if err != nil {
		s.astraRoutingMu.Unlock()
		return value, err
	}
	value = s.withAstraMembersLocked(ctx, value)
	s.astraRoutingCache = &value
	s.astraRoutingExpires = time.Now().Add(5 * time.Second)
	callback := s.astraRoutingOnSaved
	s.astraRoutingMu.Unlock()
	if callback != nil && !schedulingOnly {
		callback(value)
	}
	return value, nil
}
func (s *SettingService) SetAstraRoutingOnSaved(callback func(config.AstraRoutingSettings)) {
	s.astraRoutingMu.Lock()
	defer s.astraRoutingMu.Unlock()
	s.astraRoutingOnSaved = callback
}

// SetAstraAccountLister supplies the active OpenAI accounts from which
// targets and WS members are derived. Without it both lists stay empty.
func (s *SettingService) SetAstraAccountLister(list func(context.Context) ([]Account, error)) {
	s.astraRoutingMu.Lock()
	defer s.astraRoutingMu.Unlock()
	s.astraAccounts = list
	s.astraRoutingExpires = time.Time{}
}

func (s *SettingService) astraRoutingRuntime(ctx context.Context) config.AstraRoutingSettings {
	s.astraRoutingMu.Lock()
	defer s.astraRoutingMu.Unlock()
	if s.astraRoutingCache != nil && time.Now().Before(s.astraRoutingExpires) {
		return *s.astraRoutingCache
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := s.GetAstraRouting(ctx)
	if err != nil {
		if s.astraRoutingCache != nil {
			return *s.astraRoutingCache
		}
		return config.AstraRoutingSettings{}
	}
	value = s.withAstraMembersLocked(ctx, value)
	s.astraRoutingCache = &value
	s.astraRoutingExpires = time.Now().Add(5 * time.Second)
	return value
}

// withAstraMembersLocked derives targets and WS members. A failed inventory
// read keeps the members of an unchanged configuration rather than dropping
// every borrower for a transient database error. Callers hold astraRoutingMu.
func (s *SettingService) withAstraMembersLocked(ctx context.Context, value config.AstraRoutingSettings) config.AstraRoutingSettings {
	value = value.Configured()
	if !value.CookiePool.Enabled || s.astraAccounts == nil {
		return value
	}
	scope, err := s.GetCodexTicketHarvestScope(ctx)
	var accounts []Account
	if err == nil {
		accounts, err = s.astraAccounts(ctx)
	}
	if err != nil {
		if previous := s.astraRoutingCache; previous != nil && config.AstraRouteSettingsEqual(*previous, value) && previous.Revision == value.Revision {
			value.CookiePool.TargetAccountIDs = slices.Clone(previous.CookiePool.TargetAccountIDs)
			value.WSSession.AccountIDs = slices.Clone(previous.WSSession.AccountIDs)
		}
		return value
	}
	return deriveAstraMembers(value, accounts, scope, NewOpenAIWSProtocolResolver(s.cfg))
}

// deriveAstraMembers selects every harvest-scope Codex account other than the
// sources as a borrower, and every route-pool account whose WSv2 is already
// enabled as a WS session member. Only stable account settings take part, so
// rate limits and cooldowns never churn membership.
func deriveAstraMembers(value config.AstraRoutingSettings, accounts []Account, scope CodexTicketHarvestScope, ws OpenAIWSProtocolResolver) config.AstraRoutingSettings {
	sources := value.CookiePool.SourceAccountIDs
	var targets, members []int64
	for i := range accounts {
		account := &accounts[i]
		if !isOpenAICodexTicketAccount(account, OpenAICodexStateProbeDefaultModel) {
			continue
		}
		source := slices.Contains(sources, account.ID)
		if !source {
			if openAICodexSkipHarvest(account) || !scope.includes(account) {
				continue
			}
			targets = append(targets, account.ID)
		}
		if value.WSSession.Enabled && ws.Resolve(account).Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
			members = append(members, account.ID)
		}
	}
	slices.Sort(targets)
	slices.Sort(members)
	value.CookiePool.TargetAccountIDs = slices.Compact(targets)
	value.WSSession.AccountIDs = slices.Compact(members)
	return value
}
