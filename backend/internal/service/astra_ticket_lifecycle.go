package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// AstraTicketLifecycle joins gateway reuse to the account ticket store. It is
// installed at construction, before either background collection or HTTP starts.
// Ticket values are applied directly to request headers; public DTOs contain
// only readiness metadata, never credential or routing cookie values.
type AstraTicketLifecycle interface {
	UnifiedAstraTicketsEnabled(context.Context) bool
	PrepareStoredAstraSources(context.Context) error
	VerifyStoredAstraTarget(context.Context, int64) error
	ApplyStoredAstraTarget(context.Context, http.Header, int64, string) (string, func(), error)
	StoredAstraSnapshot(context.Context) AstraGatewayRuntime
}

type boundAstraTicketContextKey struct{}

// IsBoundAstraTicketRequest prevents a later transport wrapper from replacing
// one half of a verified credential bundle. This marker is never an HTTP header.
func IsBoundAstraTicketRequest(ctx context.Context) bool {
	bound, _ := ctx.Value(boundAstraTicketContextKey{}).(bool)
	return bound
}

func (s *OpenAIGatewayService) UnifiedAstraTicketsEnabled(ctx context.Context) bool {
	return s != nil && s.cfg != nil && s.accountRepo != nil &&
		s.openAICodexTicketEnabledContext(ctx) && s.openAICodexTicketConfig().TargetLength == 780 &&
		s.openAICodexTicketGatedModel(OpenAICodexStateProbeDefaultModel) && s.cfg.AstraRouting(ctx).CookiePool.Enabled
}

func astraTicketAccountEligible(ctx context.Context, a *Account) bool {
	// Explicit preparation can restore a target paused by Astra scheduling, but
	// never bypasses authentication, expiry, transport or upstream cooldowns.
	if a == nil || !a.IsActive() || !isOpenAICodexTicketAccount(a, OpenAICodexStateProbeDefaultModel) || a.ParentAccountID != nil {
		return false
	}
	now := time.Now()
	return (a.ExpiresAt == nil || now.Before(*a.ExpiresAt)) && !a.IsRateLimited() && !a.IsOverloaded() &&
		(a.TempUnschedulableUntil == nil || !now.Before(*a.TempUnschedulableUntil)) &&
		!a.isModelRateLimitedWithContext(ctx, OpenAICodexStateProbeDefaultModel)
}

func lendableAstraTicket(a *Account, ticket *openAICodexTicket, now time.Time) bool {
	return ticket.valid(now, 780) && ticket.Transport == "sse" && ticketIdentityMatches(a, ticket) &&
		openAICodexTicketRemainingUntil(ticket).Sub(now) > 30*time.Second
}

// storedAstraDonor returns a ticket that passed its candy check: sources
// first, then tickets other borrowers harvested themselves. A borrowed ticket
// is never lent on. With a reserve it may spend one request checking one
// untested candidate, preferring sources; without one it never sends a request.
func (s *OpenAIGatewayService) storedAstraDonor(ctx context.Context, targetID int64, reserve func() bool) (*Account, *openAICodexTicket) {
	settings := s.cfg.AstraRouting(ctx)
	var pendingAccount *Account
	var pending *openAICodexTicket
	now := time.Now()
	consider := func(id int64, member bool) (*Account, *openAICodexTicket) {
		a, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, a) {
			return nil, nil
		}
		primary := s.lookupOpenAICodexTicket(a, OpenAICodexStateProbeDefaultModel)
		if primary == nil {
			return nil, nil
		}
		var passed, untested *openAICodexTicket
		for _, ticket := range []*openAICodexTicket{primary, primary.Standby} {
			if !lendableAstraTicket(a, ticket, now) || (member && ticket.Acquisition == "reuse") {
				continue
			}
			ok, tested := s.astraCandyVerdictFor(a.ID, ticket)
			if ok && (passed == nil || openAICodexTicketRemainingUntil(ticket).After(openAICodexTicketRemainingUntil(passed))) {
				passed = ticket
			}
			if !tested && (untested == nil || openAICodexTicketRemainingUntil(ticket).After(openAICodexTicketRemainingUntil(untested))) {
				untested = ticket
			}
		}
		if passed != nil {
			return a, detachAstraDonorTicket(passed)
		}
		if untested != nil && pending == nil {
			pendingAccount, pending = a, untested
		}
		return nil, nil
	}
	for _, id := range settings.CookiePool.SourceAccountIDs {
		if id == targetID || ctx.Err() != nil {
			continue
		}
		if a, ticket := consider(id, false); ticket != nil {
			return a, ticket
		}
	}
	for _, id := range settings.CookiePool.TargetAccountIDs {
		if id == targetID || ctx.Err() != nil || slices.Contains(settings.CookiePool.SourceAccountIDs, id) ||
			!s.astraMemberMayLend(id, now, pending == nil) {
			continue
		}
		if a, ticket := consider(id, true); ticket != nil {
			return a, ticket
		}
	}
	if pending == nil || reserve == nil || !s.checkAstraDonorCandy(ctx, pendingAccount, pending, reserve) {
		return nil, nil
	}
	return pendingAccount, detachAstraDonorTicket(pending)
}

// astraMemberMayLend screens a borrower's cached ticket before an account
// read, since every harvest-scope account is a potential lender. A ticket not
// yet loaded into this process is skipped until the collector reads it.
func (s *OpenAIGatewayService) astraMemberMayLend(id int64, now time.Time, untestedUseful bool) bool {
	s.openaiCodexTicketStateMu.Lock()
	raw, _ := s.openaiCodexTickets.Load(openAICodexTicketKey(id, OpenAICodexStateProbeDefaultModel))
	s.openaiCodexTicketStateMu.Unlock()
	primary, _ := raw.(*openAICodexTicket)
	if primary == nil {
		return false
	}
	for _, ticket := range []*openAICodexTicket{primary, primary.Standby} {
		if !ticket.valid(now, 780) || ticket.Transport != "sse" || ticket.Acquisition == "reuse" ||
			openAICodexTicketRemainingUntil(ticket).Sub(now) <= 30*time.Second {
			continue
		}
		if passed, tested := s.astraCandyVerdictFor(id, ticket); passed || (!tested && untestedUseful) {
			return true
		}
	}
	return false
}

func detachAstraDonorTicket(ticket *openAICodexTicket) *openAICodexTicket {
	copy := *ticket
	copy.HarvestCookies = slices.Clone(ticket.HarvestCookies)
	return &copy
}

func (s *OpenAIGatewayService) PrepareStoredAstraSources(ctx context.Context) error {
	if !s.UnifiedAstraTicketsEnabled(ctx) {
		return errors.New("cookie_pool_disabled")
	}
	if _, ticket := s.storedAstraDonor(ctx, 0, nil); ticket != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	controls, _ := s.harvestControls(ctx)
	round := &codexHarvestRound{limit: controls.Speed.MaxRequestsPerRound}
	ctx = context.WithValue(ctx, codexHarvestRoundKey{}, round)
	reserve := func() bool { return round.Take(controls.Speed.MaxRequestsPerRound) }
	if _, ticket := s.storedAstraDonor(ctx, 0, reserve); ticket != nil {
		return nil
	}
	// Reuse is the fast path. When it has no donor, use the existing self
	// collector for source accounts rather than creating a second ticket store.
	for _, id := range s.cfg.AstraRouting(ctx).CookiePool.SourceAccountIDs {
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			continue
		}
		s.probeOnceOpenAICodexTicket(ctx, account, OpenAICodexStateProbeDefaultModel)
		if _, ticket := s.storedAstraDonor(ctx, 0, reserve); ticket != nil {
			return nil
		}
	}
	return errors.New("no_qualified_source_route")
}

// astraBorrowerAdmitted keeps a borrow bound to the configuration it started
// under and to the account still being a derived borrower.
func (s *OpenAIGatewayService) astraBorrowerAdmitted(ctx context.Context, settings config.AstraRoutingSettings, id int64) bool {
	current := s.cfg.AstraRouting(ctx)
	return config.AstraRouteSettingsEqual(settings, current) && slices.Contains(current.CookiePool.TargetAccountIDs, id)
}

// tryBorrowStoredAstraTicket runs at most one donor candidate, preceded by at
// most one donor candy check. The caller owns the harvest lock and request
// budget; failure may fall back to self collection.
func (s *OpenAIGatewayService) tryBorrowStoredAstraTicket(ctx context.Context, account *Account, model string, timeout time.Duration, reserve func() bool) (bool, codexHarvestProbeResult) {
	var result codexHarvestProbeResult
	settings := s.cfg.AstraRouting(ctx)
	if !s.UnifiedAstraTicketsEnabled(ctx) || model != OpenAICodexStateProbeDefaultModel ||
		!slices.Contains(settings.CookiePool.TargetAccountIDs, account.ID) {
		return false, result
	}
	controls, configured := s.harvestControls(ctx)
	if controls.Transport != "" && controls.Transport != "sse" {
		return false, result // SSE admission never qualifies a WebSocket ticket.
	}
	donor, seed := s.storedAstraDonor(ctx, account.ID, reserve)
	if seed == nil {
		return false, result
	}
	if !s.waitHarvestPace(ctx, controls, configured) {
		result.Kind = "cancelled"
		return true, result
	}
	result.Kind = "account_error"
	if !astraTicketAccountEligible(ctx, account) || s.codexTicketChatHeld(account.ID) {
		return true, result
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, seed, "")
	if err != nil || proxy == "" {
		release()
		result.Kind = "network_error"
		return true, result
	}
	defer release()
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || token == "" {
		return true, result
	}
	identity := ticketIdentity(account)
	admitted := func() bool {
		if ctx.Err() != nil || !seed.valid(time.Now(), 780) || !s.UnifiedAstraTicketsEnabled(ctx) ||
			!s.astraBorrowerAdmitted(ctx, settings, account.ID) || s.codexTicketChatHeld(account.ID) {
			return false
		}
		fresh, e := s.accountRepo.GetByID(ctx, account.ID)
		return e == nil && astraTicketAccountEligible(ctx, fresh) && ticketIdentity(fresh) == identity && (reserve == nil || reserve())
	}
	session := uuid.NewString()
	ctx = context.WithValue(ctx, codex780SeedContextKey{}, codex780Seed{state: seed.State, cookies: seed.HarvestCookies})
	ctx = context.WithValue(ctx, codexMintEdgeContextKey{}, seed.EdgeIP)
	result = s.executeCodexHarvestProbe(ctx, account, token, model, proxy, timeout, admitted, session)
	recordCodexHarvestProbe(account, model, result.Kind, seed.HarvestNodeName, safeCodexHarvestError(result.Err), result.Status, len(result.State), result.Shape.Blocks, 780, 33, result.ProbeVerified)
	if result.Kind != "success" {
		return true, result
	}
	fresh, err := s.accountRepo.GetByID(ctx, account.ID)
	if ctx.Err() != nil || err != nil || !astraTicketAccountEligible(ctx, fresh) || ticketIdentity(fresh) != identity ||
		!s.UnifiedAstraTicketsEnabled(ctx) || !s.astraBorrowerAdmitted(ctx, settings, account.ID) {
		result.Kind = "cancelled"
		return true, result
	}
	ticket := codexHarvestTicket(fresh, model, result, s.openAICodexTicketConfig(), 2)
	ticket.HarvestProxyURL, ticket.HarvestNodeID = seed.HarvestProxyURL, seed.HarvestNodeID
	ticket.HarvestNodeName, ticket.HarvestNodeProvider = seed.HarvestNodeName, seed.HarvestNodeProvider
	ticket.HarvestSessionID = session
	ticket.Acquisition, ticket.SourceAccountID = "reuse", donor.ID
	if err = s.storeOpenAICodexTicket(ctx, fresh, ticket); err != nil {
		// The legacy store retains memory on a database error. Reuse must not
		// publish a new ready target until the complete bundle is persisted.
		s.openaiCodexTicketStateMu.Lock()
		if current, ok := s.openaiCodexTickets.Load(openAICodexTicketKey(fresh.ID, model)); ok {
			if value, ok := current.(*openAICodexTicket); ok {
				copy := *value
				if copy.State == ticket.State {
					copy.Revoked = true
				} else if copy.Standby != nil && copy.Standby.State == ticket.State {
					copy.Standby = nil
				}
				s.openaiCodexTickets.Store(openAICodexTicketKey(fresh.ID, model), &copy)
			}
		}
		s.openaiCodexTicketStateMu.Unlock()
		result.Kind = "persist_failed"
		return true, result
	}
	s.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(fresh.ID, model))
	logger.L().Info("openai_codex_ticket borrowed",
		zap.Int64("account_id", fresh.ID),
		zap.Int64("source_account_id", donor.ID),
		zap.String("source_account_name", donor.Name),
		zap.String("model", model),
		zap.String("node", seed.HarvestNodeName),
		zap.String("gateway", ticket.Gateway),
	)
	return true, result
}

func (s *OpenAIGatewayService) acquireStoredAstraTarget(ctx context.Context, id int64, protocol string) (*Account, *openAICodexTicket, error) {
	if !s.UnifiedAstraTicketsEnabled(ctx) || !slices.Contains(s.cfg.AstraRouting(ctx).CookiePool.TargetAccountIDs, id) {
		return nil, nil, errors.New("configuration_changed")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) {
		return nil, nil, errors.New("account_unavailable")
	}
	if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
		return account, ticket, nil
	}
	if protocol != "sse" {
		return nil, nil, ErrOpenAICodexTicketUnavailable
	}
	if _, ticket := s.storedAstraDonor(ctx, id, nil); ticket == nil {
		if err := s.PrepareStoredAstraSources(ctx); err != nil {
			return nil, nil, err
		}
		account, err = s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			return nil, nil, errors.New("account_unavailable")
		}
		if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
			return account, ticket, nil
		}
	}
	if !s.codexHarvestRunMu.TryLock() {
		return nil, nil, errors.New("target_validation_in_progress")
	}
	defer s.codexHarvestRunMu.Unlock()
	// A collector may have finished between the first read and acquisition.
	if ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel); ticket.valid(time.Now(), 780) && ticket.Transport == protocol {
		return account, ticket, nil
	}
	if s.ticketProbeCoolingDown(id, OpenAICodexStateProbeDefaultModel, time.Now()) {
		return nil, nil, errors.New("source_probe_cooldown")
	}
	budget := 3 // Mint and continuation, plus a candy check if the donor changed meanwhile.
	controls, _ := s.harvestControls(ctx)
	timeout := time.Duration(controls.Speed.AttemptTimeoutSeconds) * time.Second
	if timeout < time.Second {
		timeout = 25 * time.Second
	}
	attempted, result := s.tryBorrowStoredAstraTicket(ctx, account, OpenAICodexStateProbeDefaultModel, timeout, func() bool {
		if budget <= 0 {
			return false
		}
		budget--
		return true
	})
	if !attempted {
		return nil, nil, errors.New("no_qualified_source_route")
	}
	if result.Kind != "success" {
		if ctx.Err() == nil {
			cooldown := astraTicketCooldown(controls.Speed.CooldownSeconds, result.RetryAfter)
			s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(id, OpenAICodexStateProbeDefaultModel), time.Now().Add(cooldown))
		}
		return nil, nil, errors.New("target_probe_failed")
	}
	ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
	if !ticket.valid(time.Now(), 780) || ticket.Transport != protocol {
		return nil, nil, ErrOpenAICodexTicketUnavailable
	}
	return account, ticket, nil
}

func (s *OpenAIGatewayService) VerifyStoredAstraTarget(ctx context.Context, id int64) error {
	_, _, err := s.acquireStoredAstraTarget(ctx, id, "sse")
	return err
}

func (s *OpenAIGatewayService) ApplyStoredAstraTarget(ctx context.Context, h http.Header, id int64, protocol string) (string, func(), error) {
	// Forwarding only consumes an existing bundle. Acquisition and validation
	// belong to the collector/explicit diagnostics, never the caller's request.
	if !s.UnifiedAstraTicketsEnabled(ctx) || !slices.Contains(s.cfg.AstraRouting(ctx).CookiePool.TargetAccountIDs, id) {
		return "", func() {}, errors.New("configuration_changed")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || !astraTicketAccountEligible(ctx, account) {
		return "", func() {}, errors.New("account_unavailable")
	}
	ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
	if !ticket.valid(time.Now(), 780) || ticket.Transport != protocol {
		return "", func() {}, ErrOpenAICodexTicketUnavailable
	}
	// Never combine another workspace with the caller's credentials. The
	// access-token provider may have refreshed a token since this DB snapshot.
	if h.Get("ChatGPT-Account-ID") != account.GetCredential("chatgpt_account_id") || h.Get("Authorization") == "" {
		return "", func() {}, errors.New("configuration_changed")
	}
	proxy, release, err := s.pinStoredCodexTicketEgress(ctx, ticket, "")
	if err != nil {
		return "", func() {}, err
	}
	h.Set(openAICodexTurnStateHeader, ticket.State)
	restoreBoundCodexTicketHarvestIdentity(h, ticket)
	releaseChat := s.holdCodexTicketChat(account)
	return proxy, func() { release(); releaseChat() }, nil
}

func (s *OpenAIGatewayService) StoredAstraSnapshot(ctx context.Context) AstraGatewayRuntime {
	settings := s.cfg.AstraRouting(ctx)
	now := time.Now()
	out := AstraGatewayRuntime{GeneratedAt: now, Revision: settings.Revision, Sources: []AstraRouteStatus{}, Targets: []AstraRouteStatus{}, WS: []AstraWSStatus{}}
	project := func(id int64, target bool) AstraRouteStatus {
		r := AstraRouteStatus{AccountID: id, State: "waiting", Reason: "target_not_verified"}
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !astraTicketAccountEligible(ctx, account) {
			return r
		}
		ticket := s.lookupOpenAICodexTicket(account, OpenAICodexStateProbeDefaultModel)
		if !ticket.valid(now, 780) {
			return r
		}
		expires := openAICodexTicketRemainingUntil(ticket)
		r.State, r.Reason = "ready", "target_probe_passed"
		if !target {
			r.State, r.Reason = "candidate", "source_passed_pending_target"
			passed, tested := s.astraCandyVerdictFor(id, ticket)
			if standby := ticket.Standby; !passed && standby.valid(now, 780) {
				standbyPassed, standbyTested := s.astraCandyVerdictFor(id, standby)
				passed, tested = standbyPassed, tested && standbyTested
			}
			if !passed {
				r.State, r.Reason = "waiting", "source_candy_pending"
				if tested {
					r.Reason = "source_candy_failed"
				}
			}
		}
		r.CheckedAt, r.ExpiresAt = &ticket.CapturedAt, &expires
		r.RemainingSeconds = int64(expires.Sub(now).Seconds())
		r.Gateway = "chat.gateway." + ticket.Gateway + ".api.openai.com"
		r.ProxyNode = ticket.HarvestNodeName
		return r
	}
	for _, id := range settings.CookiePool.SourceAccountIDs {
		out.Sources = append(out.Sources, project(id, false))
	}
	for _, id := range settings.CookiePool.TargetAccountIDs {
		r := project(id, true)
		out.Targets = append(out.Targets, r)
		if r.State == "ready" {
			out.ReadyRoutes++
			if out.RouteExpiresAt == nil || (r.ExpiresAt != nil && r.ExpiresAt.Before(*out.RouteExpiresAt)) {
				out.RouteExpiresAt = r.ExpiresAt
			}
		}
	}
	return out
}

// Compile-time assertions keep the repository adapter independent of service internals.
var _ AstraTicketLifecycle = (*OpenAIGatewayService)(nil)

func astraTicketCooldown(seconds int, retry time.Duration) time.Duration {
	d := time.Duration(seconds) * time.Second
	if retry > d {
		d = retry
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}
