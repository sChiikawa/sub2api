package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type astraTicketRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts map[int64]*Account
	writes   int
	err      error
}

func (r *astraTicketRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.accounts[id]; a != nil {
		copy := *a
		return &copy, nil
	}
	return nil, errors.New("missing fixture")
}
func (r *astraTicketRepo) UpdateExtra(_ context.Context, id int64, extra map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.writes++
	a := *r.accounts[id]
	a.Extra = MergeOpenAICodexTicketExtra(extra, a.Extra)
	// UpdateExtra merges keys; preserve a detached account snapshot in this mock.
	for key, value := range extra {
		a.Extra[key] = value
	}
	r.accounts[id] = &a
	return nil
}

func unifiedAstraFixture(t *testing.T) (*OpenAIGatewayService, *astraTicketRepo, *openAICodexTicket) {
	t.Helper()
	a, b := ticketTestAccount(1), ticketTestAccount(2)
	a.Credentials = map[string]any{"access_token": "source-token", "chatgpt_account_id": "source-workspace"}
	b.Credentials = map[string]any{"access_token": "target-token", "chatgpt_account_id": "target-workspace"}
	repo := &astraTicketRepo{accounts: map[int64]*Account{1: a, 2: b}}
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, TTLSeconds: 240, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://self.invalid:8080", HarvestAttemptTimeoutSeconds: 10}, nil)
	s.accountRepo = repo
	s.cfg.Gateway.CodexGatewayPin = config.CodexGatewayPinConfig{Enabled: true, SourceAccountIDs: []int64{1}, TargetAccountIDs: []int64{2}, IPAffinity: true, TTLSeconds: 240}
	now := time.Now().Add(-5 * time.Second)
	seed := &openAICodexTicket{AccountID: 1, Model: "gpt-6-astra", State: mint780State(now), Length: 780, Blocks: 33, IssuedAt: now, CapturedAt: now, ExpiresAt: now.Add(240 * time.Second), ProbeVerified: true, Transport: "sse", Gateway: "unified-42", HarvestCookies: mint780Pair(time.Now().Add(time.Hour), "unified-42"), HarvestProxyURL: "http://source.invalid:8080"}
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), a, seed))
	s.recordAstraCandyVerdict(1, seed, "candy_passed")
	repo.writes = 0
	return s, repo, seed
}

func targetTicketHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer target-token")
	h.Set("ChatGPT-Account-ID", "target-workspace")
	h.Set("Cookie", "__oailb=unrelated; __cflb=unrelated")
	h.Set(openAICodexTurnStateHeader, "unrelated")
	return h
}

func TestUnifiedAstraVerifyDoesNotMintBorrowedTicket(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		calls++
		t.Fatal("verification must not harvest a borrowed ticket onto the account")
		return nil, nil
	}}
	resetCodexHarvestFlow()
	require.NoError(t, s.VerifyStoredAstraTarget(t.Context(), 2))
	require.NoError(t, s.VerifyStoredAstraTarget(t.Context(), 2))
	require.Zero(t, calls)
	require.Zero(t, repo.writes)
	for _, event := range listCodexHarvestFlowEvents() {
		require.NotEqual(t, "borrow", event.Kind)
	}
	account, _ := repo.GetByID(t.Context(), 2)
	require.False(t, s.lookupOpenAICodexTicket(account, "gpt-6-astra").valid(time.Now(), 780))
	h := targetTicketHeaders()
	proxy, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.NoError(t, err)
	release()
	require.Empty(t, proxy)
	require.Equal(t, seed.State, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, strings.Join(seed.HarvestCookies, "; "), h.Get("Cookie"))
	require.Equal(t, "Bearer target-token", h.Get("Authorization"))
	require.True(t, astraAccountSchedulingReady(s.cfg.AstraRouting(t.Context()), s.StoredAstraSnapshot(t.Context()), 2, time.Now()))
	public, err := json.Marshal(s.StoredAstraSnapshot(t.Context()))
	require.NoError(t, err)
	for _, secret := range []string{seed.State, "source-token", "target-token", "source.invalid"} {
		require.NotContains(t, string(public), secret)
	}
}

func TestUnifiedAstraVerifyStopsWhenAccountIsNotABorrower(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		calls++
		t.Fatal("leaving the borrower set must not start a harvest")
		return nil, nil
	}}
	s.cfg.Gateway.CodexGatewayPin.TargetAccountIDs = nil
	err := s.VerifyStoredAstraTarget(t.Context(), 2)
	require.EqualError(t, err, "configuration_changed")
	require.Zero(t, calls)
	require.Zero(t, repo.writes)
}

func TestUnifiedAstraReuseBudgetAndProtocol(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls++
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	_, _, err := s.ApplyStoredAstraTarget(t.Context(), targetTicketHeaders(), 2, "websocket")
	require.Error(t, err)
	require.Zero(t, calls)
	left := 1
	a, _ := repo.GetByID(t.Context(), 2)
	ok, result := s.tryBorrowStoredAstraTicket(t.Context(), a, "gpt-6-astra", time.Second, func() bool {
		if left == 0 {
			return false
		}
		left--
		return true
	})
	require.True(t, ok)
	require.NotEqual(t, "success", result.Kind)
	require.Equal(t, 1, calls)
	require.Zero(t, repo.writes)
	seed.Revoked = true
	repo.accounts[1].Extra["codex_skip_harvest"] = true
	require.Error(t, s.PrepareStoredAstraSources(t.Context()))
}

func TestUnifiedAstraSourceBootstrapUsesSameStoreAndBudget(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	s.openaiCodexTickets.Delete(openAICodexTicketKey(1, "gpt-6-astra"))
	repo.accounts[1].Extra = nil
	s.cfg.Gateway.OpenAICodexTicket.MaxProbesPerRound = 3
	calls := 0
	state := mint780State(time.Now())
	cookies := mint780Pair(time.Now().Add(time.Hour), "unified-77")
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Equal(t, "Bearer source-token", req.Header.Get("Authorization"))
		require.Equal(t, "http://self.invalid:8080", proxy)
		h := http.Header{}
		body := harvestProbeCompleteFixture("gpt-6-astra")
		switch calls {
		case 1:
			require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
			h.Set(openAICodexTurnStateHeader, state)
			h["Set-Cookie"] = cookies
		case 2:
			require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
		default:
			require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, "source-workspace", req.Header.Get("ChatGPT-Account-ID"))
			raw, _ := io.ReadAll(req.Body)
			require.Contains(t, string(raw), CandyPrompt[:24], "the new source ticket is candy-checked before lending")
			body = candyStream("21")
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	require.NoError(t, s.PrepareStoredAstraSources(t.Context()))
	require.Equal(t, 3, calls)
	require.Equal(t, 2, repo.writes, "ticket and probe summary are separate existing writes; the candy verdict is not persisted")
	_, donor := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", nil)
	require.Equal(t, state, donor.State)
	require.Equal(t, "self", donor.Acquisition)
	require.NoError(t, s.PrepareStoredAstraSources(t.Context()))
	require.Equal(t, 3, calls, "ready source must not be recollected or re-checked")
}

func TestUnifiedAstraLendsOnlyCandyPassedSourceTickets(t *testing.T) {
	for _, answer := range []string{"21", "20"} {
		t.Run(answer, func(t *testing.T) {
			s, repo, seed := unifiedAstraFixture(t)
			s.astraCandyVerdicts = nil
			calls := 0
			s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
				calls++
				require.Equal(t, 1, calls, "an untested donor costs exactly one candy request, then no borrow on failure")
				require.Equal(t, "Bearer source-token", req.Header.Get("Authorization"))
				require.Equal(t, seed.State, req.Header.Get(openAICodexTurnStateHeader))
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(candyStream(answer)))}, nil
			}}
			_, donor := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", nil)
			require.Nil(t, donor, "without a reserve no candy request is sent")
			require.Zero(t, calls)
			reserved := 0
			_, donor = s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", func() bool { reserved++; return true })
			require.Equal(t, 1, reserved)
			require.Equal(t, answer == "21", donor != nil)
			_, again := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", func() bool { reserved++; return true })
			require.Equal(t, 1, calls, "the verdict is reused for the ticket lifetime")
			require.Equal(t, answer == "21", again != nil)
			snapshot := s.StoredAstraSnapshot(t.Context())
			if answer == "21" {
				require.Equal(t, "source_passed_pending_target", snapshot.Sources[0].Reason)
			} else {
				require.Equal(t, "source_candy_failed", snapshot.Sources[0].Reason)
			}
			require.Zero(t, repo.writes)
		})
	}
}

func TestUnifiedAstraCandyBudgetRefusalLeavesTicketUntested(t *testing.T) {
	s, _, _ := unifiedAstraFixture(t)
	s.astraCandyVerdicts = nil
	s.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		t.Fatal("refused budget must not send a candy request")
		return nil, nil
	}}
	_, donor := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", func() bool { return false })
	require.Nil(t, donor)
	require.Equal(t, "source_candy_pending", s.StoredAstraSnapshot(t.Context()).Sources[0].Reason)
}

func TestUnifiedAstraLendsCandyPassedSelfTicketsOfOtherBorrowers(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	member := ticketTestAccount(3)
	member.Credentials = map[string]any{"access_token": "member-token", "chatgpt_account_id": "member-workspace"}
	repo.accounts[3] = member
	s.cfg.Gateway.CodexGatewayPin.TargetAccountIDs = []int64{2, 3}
	issued := time.Now().Add(-2 * time.Second)
	own := &openAICodexTicket{Acquisition: "self", AccountID: 3, Model: "gpt-6-astra", State: mint780State(issued), Length: 780, Blocks: 33, IssuedAt: issued, CapturedAt: issued, ExpiresAt: issued.Add(240 * time.Second), ProbeVerified: true, Transport: "sse", Gateway: "unified-43", HarvestCookies: mint780Pair(time.Now().Add(time.Hour), "unified-43"), HarvestProxyURL: "http://member.invalid:8080"}
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), member, own))

	donor, _ := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", nil)
	require.Equal(t, int64(1), donor.ID, "a passed source ticket is preferred")

	repo.accounts[1].Status = StatusDisabled
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		calls++
		require.Equal(t, "Bearer member-token", req.Header.Get("Authorization"))
		require.Equal(t, own.State, req.Header.Get(openAICodexTurnStateHeader))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(candyStream("21")))}, nil
	}}
	_, ticket := s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", nil)
	require.Nil(t, ticket, "an untested borrower ticket is not lent")
	donor, ticket = s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", func() bool { return true })
	require.Equal(t, 1, calls)
	require.Equal(t, int64(3), donor.ID)
	require.Equal(t, own.State, ticket.State)
	donor, _ = s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", nil)
	require.Equal(t, int64(3), donor.ID, "the verdict is reused")
	_, ticket = s.storedAstraDonor(t.Context(), 3, "gpt-6-astra", nil)
	require.Nil(t, ticket, "an account never borrows its own ticket")

	s.openaiCodexTickets.Delete(openAICodexTicketKey(3, "gpt-6-astra"))
	repo.accounts[3].Extra = nil
	borrowed := *own
	borrowed.State, borrowed.Acquisition, borrowed.SourceAccountID = mint780State(issued.Add(time.Second)), "reuse", 1
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), repo.accounts[3], &borrowed))
	s.recordAstraCandyVerdict(3, &borrowed, "candy_passed")
	_, ticket = s.storedAstraDonor(t.Context(), 2, "gpt-6-astra", func() bool { return true })
	require.Nil(t, ticket, "a borrowed ticket is never lent on")
	require.Equal(t, 1, calls)
}

func TestUnifiedAstraObservesTicketInstalledOnClonedBusinessRequest(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	account, _ := repo.GetByID(t.Context(), 2)
	state := mint780State(time.Now())
	body := &codexTicketHeaderOnlyBody{}
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		calls++
		req = req.Clone(req.Context())
		// Emulate the fast adapter applying its bundle after requesttiming
		// cloned the original business request's header map.
		ticket := *seed
		ticket.Identity = ""
		ticket.AccountID, ticket.State = account.ID, state
		require.NoError(t, s.storeOpenAICodexTicket(req.Context(), account, &ticket))
		req.Header.Set(openAICodexTurnStateHeader, state)
		return &http.Response{StatusCode: 200, Request: req, Header: http.Header{http.CanonicalHeaderKey(openAICodexTurnStateHeader): []string{"malformed"}}, Body: body}, nil
	}}
	values := map[string]string{SettingKeyOpenAICodexTicketStrict: "true"}
	s.settingService = NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: values}}, s.cfg)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	resp, err := s.doOpenAIUpstream(req, "", account)
	require.ErrorIs(t, err, ErrCodexTicketResponseRejected)
	require.Nil(t, resp)
	require.Equal(t, 1, calls)
	require.Zero(t, body.reads)
	require.Equal(t, 1, body.closes)
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader), "trace clone must remain independent")
	require.False(t, s.lookupOpenAICodexTicket(account, "gpt-6-astra").valid(time.Now(), 780))
}

func TestUnifiedAstraOwnTicketWinsAndHarvestLockPreventsDuplicateAcquisition(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	s.codexHarvestRunMu.Lock()
	err := s.VerifyStoredAstraTarget(t.Context(), 2)
	s.codexHarvestRunMu.Unlock()
	require.NoError(t, err)
	account, _ := repo.GetByID(t.Context(), 2)
	own := *seed
	own.AccountID = 2
	own.Identity = ""
	own.State = mint780State(time.Now())
	own.HarvestProxyURL = "http://own.invalid:8080"
	own.Acquisition = "self"
	own.HarvestCookies = mint780Pair(time.Now().Add(time.Hour), "unified-99")
	own.Gateway = "unified-99"
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), account, &own))
	bound := false
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		bound = IsBoundAstraTicketRequest(req.Context())
		require.Equal(t, seed.State, req.Header.Get(openAICodexTurnStateHeader))
		require.Equal(t, strings.Join(seed.HarvestCookies, "; "), req.Header.Get("Cookie"))
		require.Equal(t, "http://account.invalid:8080", proxy)
		require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	h := targetTicketHeaders()
	proxy, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.NoError(t, err)
	release()
	require.Empty(t, proxy)
	require.Equal(t, seed.State, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, strings.Join(seed.HarvestCookies, "; "), h.Get("Cookie"))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	req.Header = targetTicketHeaders()
	resp, err := s.doOpenAIUpstream(req, "http://account.invalid:8080", account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.True(t, bound)
	until := time.Now().Add(time.Hour)
	repo.accounts[2].RateLimitResetAt = &until
	require.Error(t, s.VerifyStoredAstraTarget(t.Context(), 2), "an existing ticket must not clear cooldowns")
}

func TestUnifiedAstraBackgroundHarvestsOwnTicketWhileCandyTicketIsLent(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	calls := 0
	self := mint780State(time.Now().Add(-time.Second))
	selfCookies := mint780Pair(time.Now().Add(time.Hour), "unified-99")
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.NotEqual(t, seed.State, req.Header.Get(openAICodexTurnStateHeader), "harvest must not start from a borrowed ticket")
		h := http.Header{}
		switch calls {
		case 1:
			require.Equal(t, "http://self.invalid:8080", proxy)
			require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
			require.Empty(t, req.Header.Get("Cookie"))
			h.Set(openAICodexTurnStateHeader, self)
			h["Set-Cookie"] = selfCookies
		case 2:
			require.Equal(t, self, req.Header.Get(openAICodexTurnStateHeader))
		case 3:
			require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
			require.Equal(t, self, req.Header.Get(openAICodexTurnStateHeader))
			body := candyStream("21")
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatal("self harvest exceeded the candy check")
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	account, _ := repo.GetByID(t.Context(), 2)
	s.probeOnceOpenAICodexTicket(t.Context(), account, "gpt-6-astra")
	require.Equal(t, 3, calls)
	ticket := s.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.Equal(t, self, ticket.State)
	require.Equal(t, "self", ticket.Acquisition)
	require.Zero(t, ticket.SourceAccountID)
	passed, tested := s.astraCandyVerdictFor(account.ID, ticket)
	require.True(t, tested)
	require.True(t, passed)
	require.Equal(t, seed.State, s.memoryCandyAstraTicket(2, "gpt-6-astra").State)
}

func TestUnifiedAstraBackgroundHarvestsSelfWhenNoCandyTicket(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	source, _ := repo.GetByID(t.Context(), 1)
	s.recordAstraCandyVerdict(1, s.lookupOpenAICodexTicket(source, "gpt-6-astra"), "candy_incorrect")
	calls := 0
	self := mint780State(time.Now().Add(-time.Second))
	selfCookies := mint780Pair(time.Now().Add(time.Hour), "unified-99")
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		h := http.Header{}
		switch calls {
		case 1:
			require.Equal(t, "http://self.invalid:8080", proxy)
			require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
			require.Empty(t, req.Header.Get("Cookie"))
			h.Set(openAICodexTurnStateHeader, self)
			h["Set-Cookie"] = selfCookies
		case 2:
			require.Equal(t, self, req.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, strings.Join(selfCookies, "; "), req.Header.Get("Cookie"))
		case 3:
			require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
			require.Equal(t, self, req.Header.Get(openAICodexTurnStateHeader))
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(candyStream("21")))}, nil
		default:
			t.Fatal("self harvest exceeded the candy check")
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	account, _ := repo.GetByID(t.Context(), 2)
	s.probeOnceOpenAICodexTicket(t.Context(), account, "gpt-6-astra")
	require.Equal(t, 3, calls)
	ticket := s.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.Equal(t, self, ticket.State)
	require.Equal(t, "self", ticket.Acquisition)
	require.Zero(t, ticket.SourceAccountID)
	other := ticketTestAccount(3)
	other.Credentials = map[string]any{"access_token": "other-token", "chatgpt_account_id": "other-workspace"}
	repo.accounts[3] = other
	s.cfg.Gateway.CodexGatewayPin.TargetAccountIDs = []int64{2, 3}
	donor, shared := s.storedAstraDonor(t.Context(), 3, "gpt-6-astra", nil)
	require.Equal(t, int64(2), donor.ID)
	require.Equal(t, self, shared.State)
	require.Equal(t, 3, calls, "a passed ticket is worn without another candy request")
}

func TestUnifiedAstraForwardingWearsCandyTicketWithoutProbing(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	s.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Equal(t, seed.State, req.Header.Get(openAICodexTurnStateHeader))
		require.Equal(t, strings.Join(seed.HarvestCookies, "; "), req.Header.Get("Cookie"))
		require.Equal(t, "http://account.invalid:8080", proxy)
		require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
		require.Equal(t, "target-workspace", req.Header.Get("ChatGPT-Account-ID"))
		require.False(t, IsOpenAICodexStateProbeRequest(req.Context()))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	account, _ := repo.GetByID(t.Context(), 2)
	require.False(t, s.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	require.True(t, s.ticketPoolSchedulingReady(account, s.cfg.AstraRouting(t.Context())))
	resetCodexHarvestFlow()
	snapshot := s.StoredAstraSnapshot(t.Context())
	require.Equal(t, "ready", snapshot.Targets[0].State)
	require.Equal(t, "shared_candy_ticket", snapshot.Targets[0].Reason)
	for _, cooling := range []bool{false, true} {
		if cooling {
			s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(2, "gpt-6-astra"), time.Now().Add(time.Minute))
		}
		h := targetTicketHeaders()
		proxy, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
		require.NoError(t, err)
		release()
		require.Empty(t, proxy)
		require.Equal(t, seed.State, h.Get(openAICodexTurnStateHeader))
		require.Equal(t, strings.Join(seed.HarvestCookies, "; "), h.Get("Cookie"))
		require.Equal(t, "Bearer target-token", h.Get("Authorization"))
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	req.Header = targetTicketHeaders()
	resp, err := s.doOpenAIUpstream(req, "http://account.invalid:8080", account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 1, calls)
	require.Zero(t, repo.writes)
	var borrowed []CodexHarvestFlowEvent
	for _, event := range listCodexHarvestFlowEvents() {
		if event.Kind == "business_borrow" {
			borrowed = append(borrowed, event)
		}
	}
	require.Len(t, borrowed, 1, "the same candy ticket is logged once for this account")
	require.Equal(t, "ticket", borrowed[0].Stage)
	require.Equal(t, int64(2), borrowed[0].AccountID)
	require.Equal(t, "#1", borrowed[0].Detail)
	require.Equal(t, "gpt-6-astra", borrowed[0].Model)
	require.Equal(t, "candy_passed", borrowed[0].Result)
	source, _ := repo.GetByID(t.Context(), 1)
	sourceReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	sourceReq.Header.Set("Authorization", "Bearer source-token")
	sourceReq.Header.Set("ChatGPT-Account-ID", "source-workspace")
	require.NoError(t, s.applyOpenAICodexTicket(t.Context(), source, "gpt-6-astra", sourceReq.Header))
	require.Equal(t, seed.State, sourceReq.Header.Get(openAICodexTurnStateHeader))
	s.astraCandyVerdicts = nil
	require.True(t, s.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	h := targetTicketHeaders()
	before := h.Clone()
	_, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	release()
	require.Equal(t, before, h)
	require.Equal(t, 1, calls)
}

func TestUnifiedAstraLendsSolTicketWithoutHarvestEgress(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	s.cfg.Gateway.OpenAICodexTicket.Models = []string{"gpt-6-astra", "gpt-5.6-sol"}
	source, _ := repo.GetByID(t.Context(), 1)
	sol := *seed
	sol.Model = "gpt-5.6-sol"
	sol.State = mint780State(time.Now().Add(-time.Second))
	sol.Gateway = "unified-77"
	sol.HarvestCookies = mint780Pair(time.Now().Add(time.Hour), "unified-77")
	sol.HarvestProxyURL = "http://sol-harvest.invalid:8080"
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), source, &sol))
	s.recordAstraCandyVerdict(1, &sol, "candy_passed")
	s.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	account, _ := repo.GetByID(t.Context(), 2)
	require.False(t, s.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"))
	require.Equal(t, sol.State, s.memoryCandyAstraTicket(2, "gpt-5.6-sol").State)
	require.Equal(t, seed.State, s.memoryCandyAstraTicket(2, "gpt-6-astra").State)
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Equal(t, "http://account.invalid:8080", proxy)
		require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
		require.Equal(t, sol.State, req.Header.Get(openAICodexTurnStateHeader))
		require.Equal(t, strings.Join(sol.HarvestCookies, "; "), req.Header.Get("Cookie"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	require.NoError(t, err)
	req.Header = targetTicketHeaders()
	resp, err := s.doOpenAIUpstream(req, "http://account.invalid:8080", account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 1, calls)
}
