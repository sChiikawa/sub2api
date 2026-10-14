package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type astraCandySkipObservationKey struct{}

// Observe after the business stream releases its exit. The verdict only feeds
// source-ticket lending; routing, ticket observation, account state and
// scheduling never see it. Capacity pressure drops this optional beta
// observation instead of delaying a business request.
func (s *OpenAIGatewayService) observeScheduledAstraCandy(ctx context.Context, headers http.Header, account *Account, ticket *openAICodexTicket) {
	if skip, _ := ctx.Value(astraCandySkipObservationKey{}).(bool); skip {
		return
	}
	if s.settingService == nil || account == nil || ticket == nil || ticket.Model != OpenAICodexStateProbeDefaultModel || ticket.Length != 780 || ticket.Transport != "sse" || !ticket.valid(time.Now(), 780) || IsOpenAICodexStateProbeRequest(ctx) {
		return
	}
	// A ticket borrowed from another account was already candy-checked before it
	// could be injected. Do not re-test it under the borrower's identity.
	if ticket.AccountID > 0 && ticket.AccountID != account.ID {
		return
	}
	if _, tested := s.astraCandyVerdictFor(account.ID, ticket); tested {
		return
	}
	copy := *ticket
	copy.Standby = nil
	copy.HarvestCookies = append([]string(nil), ticket.HarvestCookies...)
	encoded, _ := json.Marshal(copy)
	key := sha256.Sum256(encoded)
	now := time.Now()
	s.astraCandyMu.Lock()
	for k, expiry := range s.astraCandySeen {
		if !now.Before(expiry) {
			delete(s.astraCandySeen, k)
		}
	}
	if _, seen := s.astraCandySeen[key]; seen || s.astraCandyRunning >= 2 || len(s.astraCandySeen) >= 2048 {
		s.astraCandyMu.Unlock()
		return
	}
	if s.astraCandySeen == nil {
		s.astraCandySeen = make(map[[32]byte]time.Time)
	}
	s.astraCandySeen[key] = openAICodexTicketRemainingUntil(&copy)
	s.astraCandyRunning++
	s.astraCandyMu.Unlock()
	h := headers.Clone()
	id, name := account.ID, account.Name
	// The stream's request context is commonly cancelled immediately after close.
	// Use a separate bounded lifetime, carrying no caller cancellation/deadline.
	go func() {
		defer func() { s.astraCandyMu.Lock(); s.astraCandyRunning--; s.astraCandyMu.Unlock() }()
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if !s.settingService.CodexTicketCandyObservation(probeCtx) {
			s.astraCandyMu.Lock()
			delete(s.astraCandySeen, key)
			s.astraCandyMu.Unlock()
			return
		}
		reason := s.runScheduledAstraCandy(probeCtx, h, id, &copy)
		s.recordAstraCandyVerdict(id, &copy, reason)
		kind := "candy_fail"
		if reason == "candy_passed" {
			kind = "candy_pass"
		}
		recordCodexHarvestFlow(CodexHarvestFlowEvent{Stage: "select", Kind: kind, AccountID: id, AccountName: name, Model: copy.Model, Reason: reason, Result: reason, Detail: reason})
	}()
}

const (
	gatewayCandyModel           = "gpt-6-astra"
	gatewayCandyReasoningEffort = "low"
)

// observeGatewayRotationCandy asks the gateway just used by a business request
// one Beta candy question on gpt-6-astra at reasoning effort low. A wrong
// answer or any request error skips that gateway for this account. The
// business response is never delayed or replayed.

func (s *OpenAIGatewayService) observeGatewayRotationCandy(ctx context.Context, headers http.Header, account *Account, host, cookie string) {
	if s == nil || s.settingService == nil || account == nil || host == "" || strings.TrimSpace(cookie) == "" || !s.settingService.CodexGatewayCandyCheck(ctx) {
		return
	}
	if s.gatewayCandyAlreadyTested(account.ID, host) {
		return
	}
	model := gatewayCandyModel
	key := strconv.FormatInt(account.ID, 10) + "\x00" + host
	s.astraCandyMu.Lock()
	if s.gatewayCandyRunning >= 2 {
		s.astraCandyMu.Unlock()
		return
	}
	if s.gatewayCandyInflight == nil {
		s.gatewayCandyInflight = map[string]struct{}{}
	}
	if _, running := s.gatewayCandyInflight[key]; running {
		s.astraCandyMu.Unlock()
		return
	}
	s.gatewayCandyInflight[key] = struct{}{}
	s.gatewayCandyRunning++
	s.astraCandyMu.Unlock()
	h := headers.Clone()
	id, name := account.ID, account.Name
	go func() {
		defer func() {
			s.astraCandyMu.Lock()
			s.gatewayCandyRunning--
			delete(s.gatewayCandyInflight, key)
			s.astraCandyMu.Unlock()
		}()
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		reason := s.runGatewayCandy(probeCtx, h, id, model, cookie)
		kind := "candy_fail"
		if reason == "candy_passed" {
			kind = "candy_pass"
		}
		recordCodexHarvestFlow(CodexHarvestFlowEvent{Stage: "select", Kind: kind, AccountID: id, AccountName: name, Model: model, Node: host, Reason: reason, Result: reason, Detail: reason})
		if reason == "candy_disabled" || reason == "candy_unavailable" || reason == "candy_stale" || reason == "candy_busy" {
			return
		}
		expires := s.gatewayCookieExpires(host, cookie)
		passed := reason == "candy_passed"
		s.markGatewayCandyTestedThisRound(id, host)
		s.noteGatewayCandyTested(id, host, expires, passed)
		if passed {
			return
		}
		s.skipAccountGateway(&Account{ID: id, Name: name}, model, reason)
	}()
}

func (s *OpenAIGatewayService) gatewayCookieExpires(host, cookie string) time.Time {
	if s == nil || host == "" {
		return time.Time{}
	}
	want := strings.Split(strings.TrimSpace(cookie), "; ")
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	var exact, hostExpires time.Time
	now := time.Now()
	for _, slot := range s.astraGatewaySlots {
		if slot.Host != host || !gatewaySlotActive(slot, now) {
			continue
		}
		if hostExpires.IsZero() || slot.ExpiresAt.After(hostExpires) {
			hostExpires = slot.ExpiresAt
		}
		if slices.Equal(slot.Cookies, want) && (exact.IsZero() || slot.ExpiresAt.After(exact)) {
			exact = slot.ExpiresAt
		}
	}
	if !exact.IsZero() {
		return exact
	}
	if !hostExpires.IsZero() {
		return hostExpires
	}
	return now.Add(astraGatewayCookieTTL)
}

func (s *OpenAIGatewayService) runGatewayCandy(ctx context.Context, headers http.Header, id int64, model, cookie string) string {
	if s.settingService == nil || !s.settingService.CodexGatewayCandyCheck(ctx) {
		return "candy_disabled"
	}
	if s.accountRepo == nil || s.httpUpstream == nil {
		return "candy_unavailable"
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || account == nil || headers.Get("Authorization") == "" || !s.openAICodexTicketEnabledContext(ctx) {
		return "candy_stale"
	}
	if headers.Get("ChatGPT-Account-ID") != "" && headers.Get("ChatGPT-Account-ID") != account.GetCredential("chatgpt_account_id") {
		return "candy_stale"
	}
	shot, err := fireOpenAICodexPromptShotRequest(ctx, headers, model, "", cookie, "只输出最终答案数字，不要解释。", CandyPrompt, gatewayCandyReasoningEffort, func(req *http.Request) (*http.Response, error) {
		return s.httpUpstream.Do(req, openAIAccountProxyURL(account), account.ID, account.Concurrency)
	})
	switch {
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return "candy_timeout"
	case err != nil || shot.status != http.StatusOK || shot.streamErr != nil:
		return "candy_error"
	case shot.model != "" && shot.model != model:
		return "candy_model_mismatch"
	case !CandyAnswerCorrect(shot.answer):
		return "candy_incorrect"
	default:
		return "candy_passed"
	}
}

func (s *OpenAIGatewayService) runScheduledAstraCandy(ctx context.Context, headers http.Header, id int64, ticket *openAICodexTicket) string {
	return s.runAstraCandy(ctx, headers, id, ticket, s.settingService.CodexTicketCandyObservation)
}

// Exactly one SSE request with the captured account/ticket/cookies and its own
// lease for the recorded exit. Never pass the result through ticket response
// observation, cooldown handling, harvest persistence or failover. enabled is
// checked again after waiting for the exit lease.
func (s *OpenAIGatewayService) runAstraCandy(ctx context.Context, headers http.Header, id int64, ticket *openAICodexTicket, enabled func(context.Context) bool) string {
	if !enabled(ctx) {
		return "candy_disabled"
	}
	if s.accountRepo == nil || s.httpUpstream == nil {
		return "candy_unavailable"
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) || !ticketIdentityMatches(account, ticket) || headers.Get("Authorization") == "" || headers.Get("ChatGPT-Account-ID") != account.GetCredential("chatgpt_account_id") || !s.openAICodexTicketEnabledContext(ctx) {
		return "candy_stale"
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, ticket, openAIAccountProxyURL(account))
	if err != nil {
		release()
		return "candy_unavailable"
	}
	defer release()
	if !enabled(ctx) {
		return "candy_disabled"
	}
	if !ticket.valid(time.Now(), 780) {
		return "candy_stale"
	}
	routeChanged := false
	shot, err := fireOpenAICodexPromptShotRequest(ctx, headers, ticket.Model, ticket.State, strings.Join(ticket.HarvestCookies, "; "), "只输出最终答案数字，不要解释。", CandyPrompt, "", func(req *http.Request) (*http.Response, error) {
		req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(context.WithValue(req.Context(), boundAstraTicketContextKey{}, true)))
		restoreBoundCodexTicketHarvestIdentity(req.Header, ticket)
		pinBoundCodexTicketHarvestIdentityRequestBody(req, ticket)
		resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
		if resp != nil {
			expected := req.Cookies()
			for _, got := range resp.Cookies() {
				for _, want := range expected {
					if got.Name == want.Name && (got.Value != want.Value || got.MaxAge < 0 || (!got.Expires.IsZero() && !time.Now().Before(got.Expires))) {
						routeChanged = true
					}
				}
			}
		}
		return resp, err
	})
	switch {
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return "candy_timeout"
	case err != nil || shot.status != http.StatusOK || shot.streamErr != nil:
		return "candy_error"
	case routeChanged:
		return "candy_route_changed"
	case shot.state != "" && shot.state != ticket.State:
		return "candy_ticket_changed"
	case shot.model != ticket.Model:
		return "candy_model_mismatch"
	case !CandyAnswerCorrect(shot.answer):
		return "candy_incorrect"
	default:
		return "candy_passed"
	}
}

func astraCandyStreamAnswer(data []byte) string {
	for _, event := range openAICodexStateStreamEvents(data) {
		if gjson.GetBytes(event, "type").String() != "response.completed" {
			continue
		}
		var answer strings.Builder
		for _, item := range gjson.GetBytes(event, "response.output").Array() {
			if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" {
				continue
			}
			for _, content := range item.Get("content").Array() {
				if content.Get("type").String() == "output_text" {
					_, _ = answer.WriteString(content.Get("text").String())
				}
			}
		}
		return answer.String()
	}
	return ""
}
