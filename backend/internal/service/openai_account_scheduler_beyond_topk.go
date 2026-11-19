package service

import (
	"context"
	"sort"
)

// tryAcquireOpenAIAccountsBeyondTopK tries the scored candidates that Top-K left
// out, best first, before the caller falls back to a wait plan. A full Top-K
// therefore never queues a request while another eligible account has a free
// slot. Plans whose selection order already includes the overflow are skipped.
func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAIAccountsBeyondTopK(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	plan openAIAccountLoadPlan,
	budget *openAISelectionProbeBudget,
) (*AccountSelectionResult, bool, error) {
	if plan.includeOverflowFallback || len(plan.selectionOrder) == 0 {
		return nil, false, nil
	}
	ordered := make(map[int64]struct{}, len(plan.selectionOrder))
	for _, candidate := range plan.selectionOrder {
		if candidate.account != nil {
			ordered[candidate.account.ID] = struct{}{}
		}
	}
	overflow := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
	for _, candidate := range plan.candidates {
		if candidate.account == nil {
			continue
		}
		if _, ok := ordered[candidate.account.ID]; !ok {
			overflow = append(overflow, candidate)
		}
	}
	if len(overflow) == 0 {
		return nil, false, nil
	}
	sort.SliceStable(overflow, func(i, j int) bool {
		return isOpenAIAccountCandidateBetter(overflow[i], overflow[j])
	})
	// Same probe and DB-recheck ceiling as a cost-expanded Top-K.
	budget.enableLimit()
	return s.tryAcquireOpenAISelectionOrderWithBudget(ctx, req, overflow, budget)
}
