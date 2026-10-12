package service

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strconv"
	"time"
)

// A source ticket is lent only after one candy request with exactly that
// account, state and route cookies answered correctly. Verdicts live in this
// process for the ticket lifetime; a restart re-checks before lending again.
type astraCandyVerdict struct {
	passed bool
	until  time.Time
}

const (
	astraCandyVerdictLimit = 2048
	// Inconclusive checks (network, timeout, stale credentials) are retried
	// after this delay instead of consuming budget on every harvest round.
	astraCandyRetryDelay = time.Minute
	astraCandyCheckLease = 50 * time.Second
)

func astraCandyMaterial(accountID int64, ticket *openAICodexTicket) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(strconv.FormatInt(accountID, 10) + "\x00" + ticket.Model + "\x00" + ticket.State))
	for _, cookie := range ticket.HarvestCookies {
		_, _ = h.Write([]byte("\x00" + cookie))
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func astraCandyDefinitive(reason string) bool {
	switch reason {
	case "candy_passed", "candy_incorrect", "candy_model_mismatch", "candy_ticket_changed", "candy_route_changed":
		return true
	}
	return false
}

// astraCandyVerdictFor reports whether this exact ticket passed, and whether
// it was tested at all (including a check in flight or awaiting retry).
func (s *OpenAIGatewayService) astraCandyVerdictFor(accountID int64, ticket *openAICodexTicket) (passed, tested bool) {
	key := astraCandyMaterial(accountID, ticket)
	s.astraCandyMu.Lock()
	defer s.astraCandyMu.Unlock()
	verdict, ok := s.astraCandyVerdicts[key]
	if !ok || !time.Now().Before(verdict.until) {
		return false, false
	}
	return verdict.passed, true
}

// putAstraCandyVerdictLocked prunes expired entries and refuses new ones at
// capacity; an unrecorded ticket is simply checked again later.
func (s *OpenAIGatewayService) putAstraCandyVerdictLocked(key [32]byte, verdict astraCandyVerdict) bool {
	now := time.Now()
	for k, v := range s.astraCandyVerdicts {
		if !now.Before(v.until) {
			delete(s.astraCandyVerdicts, k)
		}
	}
	if _, exists := s.astraCandyVerdicts[key]; !exists && len(s.astraCandyVerdicts) >= astraCandyVerdictLimit {
		return false
	}
	if s.astraCandyVerdicts == nil {
		s.astraCandyVerdicts = make(map[[32]byte]astraCandyVerdict)
	}
	s.astraCandyVerdicts[key] = verdict
	return true
}

func (s *OpenAIGatewayService) recordAstraCandyVerdict(accountID int64, ticket *openAICodexTicket, reason string) {
	if reason == "candy_disabled" {
		return
	}
	verdict := astraCandyVerdict{passed: reason == "candy_passed", until: time.Now().Add(astraCandyRetryDelay)}
	if astraCandyDefinitive(reason) {
		verdict.until = openAICodexTicketRemainingUntil(ticket)
	}
	s.astraCandyMu.Lock()
	defer s.astraCandyMu.Unlock()
	s.putAstraCandyVerdictLocked(astraCandyMaterial(accountID, ticket), verdict)
}

// rememberStoredAstraTicket tests Beta candy after a ticket is accepted into
// the store. A database persist failure still counts: the in-memory ticket can
// be lent. A rejected ticket is left untested.
func (s *OpenAIGatewayService) rememberStoredAstraTicket(ctx context.Context, account *Account, ticket *openAICodexTicket, err error) {
	if s == nil || account == nil || ticket == nil {
		return
	}
	if err != nil {
		current := s.lookupOpenAICodexTicket(account, ticket.Model)
		if current == nil || (current.State != ticket.State && (current.Standby == nil || current.Standby.State != ticket.State)) {
			return
		}
	}
	s.testAstraCandyAfterStore(ctx, account, ticket, true)
}

// testAstraCandyAfterStore spends one request on a stored SSE ticket that has
// not been candy-checked. A pass lets every other non-source account wear it
// on business requests until it expires. Borrowed tickets are not tested and
// are not lent on. required keeps the check for a ticket that was just stored
// even when this round's budget is already spent; a later round will not
// replace a fresh ticket. A retry of an older ticket waits for the next round
// when the budget is gone.
func (s *OpenAIGatewayService) testAstraCandyAfterStore(ctx context.Context, account *Account, ticket *openAICodexTicket, required bool) {
	if account == nil || ticket == nil || ticket.Acquisition == "reuse" || ctx.Err() != nil || !s.UnifiedAstraTicketsEnabled(ctx) {
		return
	}
	if normalizeOpenAICodexTicketModel(ticket.Model) == "" || ticket.Length != 780 || ticket.Transport != "sse" || !ticket.ProbeVerified {
		return
	}
	if _, tested := s.astraCandyVerdictFor(account.ID, ticket); tested {
		return
	}
	s.checkAstraDonorCandy(ctx, account, ticket, func() bool {
		if ctx.Err() != nil {
			return false
		}
		round, _ := ctx.Value(codexHarvestRoundKey{}).(*codexHarvestRound)
		if round == nil {
			return true
		}
		controls, _ := s.harvestControls(ctx)
		if round.Take(controls.Speed.MaxRequestsPerRound) {
			return true
		}
		return required
	})
}

// checkAstraDonorCandy spends at most one reserved request on one untested
// source ticket. Concurrent callers for the same ticket do not duplicate it.
func (s *OpenAIGatewayService) checkAstraDonorCandy(ctx context.Context, account *Account, ticket *openAICodexTicket, reserve func() bool) bool {
	key := astraCandyMaterial(account.ID, ticket)
	s.astraCandyMu.Lock()
	if verdict, ok := s.astraCandyVerdicts[key]; ok && time.Now().Before(verdict.until) {
		s.astraCandyMu.Unlock()
		return verdict.passed
	}
	claimed := s.putAstraCandyVerdictLocked(key, astraCandyVerdict{until: time.Now().Add(astraCandyCheckLease)})
	s.astraCandyMu.Unlock()
	if !claimed {
		return false
	}
	reason := ""
	defer func() {
		if reason == "" || reason == "candy_disabled" {
			s.astraCandyMu.Lock()
			delete(s.astraCandyVerdicts, key)
			s.astraCandyMu.Unlock()
			return
		}
		s.recordAstraCandyVerdict(account.ID, ticket, reason)
	}()
	token, _, err := s.GetAccessToken(ctx, account)
	headers := http.Header{}
	if err == nil && token != "" {
		headers.Set("Authorization", "Bearer "+token)
		err = resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account)
	}
	if err != nil || token == "" {
		reason = "candy_stale"
		return false
	}
	if ctx.Err() != nil || !reserve() {
		return false
	}
	reason = s.runAstraCandy(ctx, headers, account.ID, ticket, s.UnifiedAstraTicketsEnabled)
	if reason == "candy_disabled" {
		return false
	}
	kind := "candy_fail"
	if reason == "candy_passed" {
		kind = "candy_pass"
	}
	recordCodexHarvestFlow(CodexHarvestFlowEvent{Stage: "select", Kind: kind, AccountID: account.ID, AccountName: account.Name, Model: ticket.Model, Node: codex780CookieGatewayHost(ticket.HarvestCookies), Reason: reason, Result: "lend_check", Detail: reason})
	s.recordAstraCandyGatewayHistory(account, ticket, reason)
	return reason == "candy_passed"
}

func (s *OpenAIGatewayService) recordAstraCandyGatewayHistory(account *Account, ticket *openAICodexTicket, reason string) {
	if account == nil || ticket == nil {
		return
	}
	passed := reason == "candy_passed"
	switch reason {
	case "candy_passed", "candy_incorrect", "candy_model_mismatch", "candy_ticket_changed", "candy_route_changed":
	default:
		return
	}
	host := codex780CookieGatewayHost(ticket.HarvestCookies)
	if host == "" {
		return
	}
	lastReason := "response_not_qualified"
	if passed {
		lastReason = "qualified"
	}
	s.recordAstraGatewayHistory(AstraGatewayHistoryRecord{
		Gateway:         host,
		SourceAccountID: account.ID,
		LastSeen:        time.Now(),
		LastReason:      lastReason,
	}, passed)
}
