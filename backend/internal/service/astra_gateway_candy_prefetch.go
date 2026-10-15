package service

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

const gatewayCandyPrefetchInterval = 20 * time.Second

// gatewayCandyPrefetchCount is how many accounts in one group should already
// have passed candy. used is the group's in-use concurrency, slotSize is the
// concurrency a passed account is given for its 240-second gateway turn.
func gatewayCandyPrefetchCount(used, slotSize int) int {
	if slotSize < 1 {
		slotSize = 1
	}
	if used < 0 {
		used = 0
	}
	return used / slotSize
}

func (s *OpenAIGatewayService) gatewayCandyProbeEnabled(ctx context.Context) bool {
	return s != nil && s.settingService != nil &&
		(s.settingService.CodexGatewayCandyCheck(ctx) || s.settingService.CodexGatewayCandyPrefetch(ctx))
}

func (s *OpenAIGatewayService) gatewayCandyPrefetchLoop(ctx context.Context) {
	if s == nil {
		return
	}
	s.prefetchGatewayCandy(ctx)
	ticker := time.NewTicker(gatewayCandyPrefetchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.prefetchGatewayCandy(ctx)
		}
	}
}

func (s *OpenAIGatewayService) gatewayCandyPreferSchedulingEnabled() bool {
	return s != nil && s.settingService != nil && s.settingService.CodexGatewayCandyPrefer(context.Background())
}

func (s *OpenAIGatewayService) shouldLeaveStickyForPrefetchedCandy(accountID int64) bool {
	if s == nil || s.settingService == nil || accountID <= 0 || !s.gatewayCandyPreferSchedulingEnabled() {
		return false
	}
	now := time.Now()
	if _, passed := s.candyPassedAccountSchedule(accountID, now); passed {
		return false
	}
	s.ensureAstraGatewayStore()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	for id := range s.astraGatewayCandyPass {
		if id != accountID && s.candyPassActiveLocked(id, now) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) prefetchGatewayCandy(ctx context.Context) {
	if s == nil || s.accountRepo == nil || s.settingService == nil || !s.settingService.CodexGatewayCandyPrefetch(ctx) || !s.codexGatewayRotationEnabled(ctx) {
		return
	}
	accounts, err := s.accountRepo.ListSchedulableByPlatform(ctx, PlatformOpenAI)
	if err != nil || len(accounts) == 0 {
		return
	}
	now := time.Now()
	groups := map[int64][]*Account{}
	var ids []int64
	for i := range accounts {
		account := &accounts[i]
		if !s.shouldRotateCodexOAuthGateway(ctx, account, gatewayCandyModel) {
			continue
		}
		groupIDs := accountGroupIDs(account)
		if len(groupIDs) == 0 {
			continue
		}
		ids = append(ids, account.ID)
		for _, groupID := range groupIDs {
			groups[groupID] = append(groups[groupID], account)
		}
	}
	if len(groups) == 0 {
		return
	}
	used := map[int64]int{}
	if s.concurrencyService != nil {
		if batch, loadErr := s.concurrencyService.GetAccountConcurrencyBatch(ctx, ids); loadErr == nil {
			used = batch
		}
	}
	slotSize := s.gatewayCandyConcurrency(ctx)
	for _, members := range groups {
		inUse := 0
		var passed, waiting []*Account
		for _, account := range members {
			inUse += used[account.ID]
			if _, ok := s.candyPassedAccountSchedule(account.ID, now); ok {
				passed = append(passed, account)
				continue
			}
			waiting = append(waiting, account)
		}
		need := gatewayCandyPrefetchCount(inUse, slotSize) - len(passed)
		if need <= 0 || len(waiting) == 0 {
			continue
		}
		sort.Slice(waiting, func(i, j int) bool { return waiting[i].ID < waiting[j].ID })
		for _, account := range waiting {
			if need <= 0 {
				break
			}
			switch s.startPrefetchGatewayCandy(ctx, account) {
			case prefetchCandyStarted:
				need--
			case prefetchCandyBusy:
				return
			}
		}
	}
}

type prefetchCandyResult int

const (
	prefetchCandySkip prefetchCandyResult = iota
	prefetchCandyStarted
	prefetchCandyBusy
)

func (s *OpenAIGatewayService) startPrefetchGatewayCandy(ctx context.Context, account *Account) prefetchCandyResult {
	if s == nil || account == nil {
		return prefetchCandySkip
	}
	slot, _ := s.currentAccountGateway(account, gatewayCandyModel, time.Now())
	if slot == nil || slot.Host == "" || len(slot.Cookies) == 0 {
		return prefetchCandySkip
	}
	if s.gatewayCandyAlreadyTested(account.ID, slot.Host) {
		return prefetchCandySkip
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || strings.TrimSpace(token) == "" {
		return prefetchCandySkip
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		return prefetchCandySkip
	}
	applyOpenAICodexTicketHarvestIdentity(headers, gatewayCandyModel)
	if !s.queueGatewayCandyProbe(ctx, headers, account, slot.Host, strings.Join(slot.Cookies, "; ")) {
		return prefetchCandyBusy
	}
	return prefetchCandyStarted
}

func accountGroupIDs(account *Account) []int64 {
	if account == nil {
		return nil
	}
	if len(account.GroupIDs) > 0 {
		return account.GroupIDs
	}
	ids := make([]int64, 0, len(account.AccountGroups))
	seen := map[int64]struct{}{}
	for _, group := range account.AccountGroups {
		if group.GroupID <= 0 {
			continue
		}
		if _, ok := seen[group.GroupID]; ok {
			continue
		}
		seen[group.GroupID] = struct{}{}
		ids = append(ids, group.GroupID)
	}
	return ids
}
