package service

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountGatewayRotationUsesEachHostFor240SecondsThenResets(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(7)
	account.Name = "rotating"
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-127"))
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-125"))
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-126"))
	s.rememberGatewayCookie([]string{"__cflb=route"})
	now := time.Now()
	first, switched := s.currentAccountGateway(account, "gpt-6-astra", now)
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-125.api.openai.com", first.Host)
	again, switched := s.currentAccountGateway(account, "gpt-6-astra", now.Add(239*time.Second))
	require.False(t, switched)
	require.Equal(t, first.Host, again.Host)
	second, switched := s.currentAccountGateway(account, "gpt-6-astra", now.Add(240*time.Second))
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-126.api.openai.com", second.Host)
	third, _ := s.currentAccountGateway(account, "gpt-6-astra", now.Add(480*time.Second))
	require.Equal(t, "chat.gateway.unified-127.api.openai.com", third.Host)
	other := ticketTestAccount(8)
	other.Name = "later"
	independent, _ := s.currentAccountGateway(other, "gpt-5.6-sol", now.Add(500*time.Second))
	require.Equal(t, first.Host, independent.Host)
	turns := s.collectedAstraGatewayTurnsAt(now.Add(500 * time.Second))
	require.Len(t, turns, 2)
	require.Equal(t, int64(7), turns[0].AccountID)
	require.Equal(t, "chat.gateway.unified-127.api.openai.com", turns[0].Host)
	require.Equal(t, []string{
		"chat.gateway.unified-125.api.openai.com",
		"chat.gateway.unified-126.api.openai.com",
		"chat.gateway.unified-127.api.openai.com",
	}, turns[0].UsedHosts)
	require.Equal(t, int64(8), turns[1].AccountID)
	require.Equal(t, first.Host, turns[1].Host)
	require.Equal(t, []string{first.Host}, turns[1].UsedHosts)
	reset, switched := s.currentAccountGateway(account, "gpt-6-astra", now.Add(720*time.Second))
	require.True(t, switched)
	require.Equal(t, first.Host, reset.Host)
	require.Empty(t, (&OpenAIGatewayService{}).CollectedAstraGatewayTurns())
}

func TestRotationDoesNotSkipGatewaysDiscoveredOutOfOrder(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(7)
	start := time.Now()
	remember := func(node string, at time.Time) {
		s.rememberGatewayCookieAt(mint780Pair(at.Add(2*time.Hour), node), at)
	}
	remember("unified-200", start)
	remember("unified-9", start)
	first, switched := s.currentAccountGateway(account, "gpt-6-astra", start)
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-9.api.openai.com", first.Host)
	remember("unified-50", start.Add(10*time.Second))
	held, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(30*time.Second))
	require.False(t, switched)
	require.Equal(t, first.Host, held.Host)
	second, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(240*time.Second))
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-50.api.openai.com", second.Host)
	remember("unified-10", start.Add(250*time.Second))
	third, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(480*time.Second))
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-10.api.openai.com", third.Host)
	fourth, _ := s.currentAccountGateway(account, "gpt-6-astra", start.Add(720*time.Second))
	require.Equal(t, "chat.gateway.unified-200.api.openai.com", fourth.Host)
	again, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(960*time.Second))
	require.True(t, switched)
	require.Equal(t, first.Host, again.Host)
}

func TestCollectedAstraGatewaysListsHostsWithoutCookies(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-200"))
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-9"))
	s.rememberGatewayCookie([]string{"__cflb=route"})
	list := s.CollectedAstraGateways()
	require.Equal(t, "chat.gateway.unified-9.api.openai.com", list[0].Host)
	require.Equal(t, "chat.gateway.unified-200.api.openai.com", list[1].Host)
	require.Equal(t, 1, list[0].Order)
	require.Equal(t, "active", list[0].Status)
	require.True(t, list[0].HasCookie)
	require.InDelta(t, int(time.Hour/time.Second), list[0].RemainingSeconds, 2)
	require.Equal(t, time.Hour, list[0].ExpiresAt.Sub(list[0].SavedAt))
	raw, err := json.Marshal(list)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "__oailb")
	require.NotContains(t, string(raw), "__cflb")
	require.Empty(t, (&OpenAIGatewayService{}).CollectedAstraGateways())
}

func TestGatewayCookieIsSavedForOneHour(t *testing.T) {
	store := &memoryGatewayPool{}
	BindAstraGatewayPoolRepository(store)
	t.Cleanup(func() { BindAstraGatewayPoolRepository(nil) })
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(7)
	start := time.Date(2026, 10, 12, 13, 21, 37, 0, time.UTC)
	same := mint780Pair(start.Add(2*time.Hour), "unified-125")
	s.rememberGatewayCookieFrom(7, "rotating", same, start)
	s.rememberGatewayCookieFrom(7, "rotating", same, start.Add(30*time.Minute))
	kept := s.collectedAstraGatewaysAt(start.Add(30 * time.Minute))
	require.Len(t, kept, 1)
	require.True(t, kept[0].SavedAt.Equal(start))
	require.Equal(t, 1800, kept[0].RemainingSeconds)
	require.Equal(t, int64(7), kept[0].AccountID)

	slot, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(59*time.Minute))
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-125.api.openai.com", slot.Host)
	gone, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(time.Hour))
	require.False(t, switched)
	require.Nil(t, gone)
	require.Empty(t, s.collectedAstraGatewaysAt(start.Add(time.Hour)))
	require.Empty(t, store.slots)
}

func TestArchiveExpiredGatewaysKeepsUsableOnes(t *testing.T) {
	store := &memoryGatewayPool{}
	BindAstraGatewayPoolRepository(store)
	t.Cleanup(func() { BindAstraGatewayPoolRepository(nil) })
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	start := time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	s.rememberGatewayCookieFrom(1, "old", mint780Pair(start.Add(3*time.Hour), "unified-10"), start)
	s.rememberGatewayCookieFrom(2, "retired", mint780Pair(start.Add(70*time.Minute), "unified-11"), start.Add(60*time.Minute))
	s.rememberGatewayCookieFrom(3, "fresh", mint780Pair(start.Add(3*time.Hour), "unified-12"), start.Add(90*time.Minute))
	now := start.Add(100 * time.Minute)

	before := s.collectedAstraGatewaysAt(now)
	statuses := map[string]string{}
	for _, entry := range before {
		statuses[entry.Host] = entry.Status
	}
	require.NotContains(t, statuses, "chat.gateway.unified-10.api.openai.com")
	require.NotContains(t, statuses, "chat.gateway.unified-11.api.openai.com")
	require.Equal(t, "active", statuses["chat.gateway.unified-12.api.openai.com"])
	require.Len(t, store.slots, 1)

	require.Equal(t, 0, s.archiveExpiredAstraGatewaysAt(now))
	after := s.collectedAstraGatewaysAt(now)
	require.Len(t, after, 1)
	require.Equal(t, "chat.gateway.unified-12.api.openai.com", after[0].Host)
	require.Equal(t, "active", after[0].Status)
	require.Len(t, store.slots, 1)
	require.Equal(t, 0, s.archiveExpiredAstraGatewaysAt(now))
	var missing *OpenAIGatewayService
	require.Equal(t, 0, missing.ArchiveExpiredAstraGateways())
}

type memoryGatewayPool struct {
	slots []AstraGatewayPoolSlot
	turns []AstraGatewayPoolTurn
}

func (m *memoryGatewayPool) ListSlots(context.Context) ([]AstraGatewayPoolSlot, error) {
	return append([]AstraGatewayPoolSlot(nil), m.slots...), nil
}
func (m *memoryGatewayPool) SaveSlot(_ context.Context, slot AstraGatewayPoolSlot) error {
	for i := range m.slots {
		if m.slots[i].Host == slot.Host && m.slots[i].AccountID == slot.AccountID && slices.Equal(m.slots[i].Cookies, slot.Cookies) {
			m.slots[i] = slot
			return nil
		}
	}
	m.slots = append(m.slots, slot)
	return nil
}
func (m *memoryGatewayPool) DeleteSlot(_ context.Context, slot AstraGatewayPoolSlot) error {
	kept := m.slots[:0]
	for _, item := range m.slots {
		if item.Host == slot.Host && item.AccountID == slot.AccountID && slices.Equal(item.Cookies, slot.Cookies) {
			continue
		}
		kept = append(kept, item)
	}
	m.slots = kept
	return nil
}
func (m *memoryGatewayPool) ListTurns(context.Context) ([]AstraGatewayPoolTurn, error) {
	return append([]AstraGatewayPoolTurn(nil), m.turns...), nil
}
func (m *memoryGatewayPool) SaveTurn(_ context.Context, turn AstraGatewayPoolTurn) error {
	for i := range m.turns {
		if m.turns[i].AccountID == turn.AccountID {
			m.turns[i] = turn
			return nil
		}
	}
	m.turns = append(m.turns, turn)
	return nil
}

func TestGatewayPoolAndTurnSurviveRestart(t *testing.T) {
	store := &memoryGatewayPool{}
	BindAstraGatewayPoolRepository(store)
	t.Cleanup(func() { BindAstraGatewayPoolRepository(nil) })
	cfg := config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, Models: []string{"gpt-6-astra"}}
	first := ticketTestService(t, cfg, nil)
	first.accountRepo = &astraTicketRepo{accounts: map[int64]*Account{}}
	first.cfg.Gateway.CodexGatewayPin.Enabled = true
	account := ticketTestAccount(7)
	account.Name = "rotating"
	start := time.Now()
	first.rememberGatewayCookieFrom(7, "rotating", mint780Pair(start.Add(2*time.Hour), "unified-15"), start)
	slot, switched := first.currentAccountGateway(account, "gpt-5.4", start)
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-15.api.openai.com", slot.Host)
	require.Len(t, store.slots, 1)
	require.Len(t, store.turns, 1)
	require.NotContains(t, store.slots[0].AccountName, "__oailb")

	restarted := ticketTestService(t, cfg, nil)
	restarted.accountRepo = &astraTicketRepo{accounts: map[int64]*Account{}}
	restarted.cfg.Gateway.CodexGatewayPin.Enabled = true
	list := restarted.CollectedAstraGateways()
	require.Len(t, list, 1)
	require.Equal(t, "chat.gateway.unified-15.api.openai.com", list[0].Host)
	require.Equal(t, int64(7), list[0].AccountID)
	again, switched := restarted.currentAccountGateway(account, "gpt-5.4", start.Add(time.Minute))
	require.False(t, switched)
	require.Equal(t, slot.Host, again.Host)
}

func TestGatewayRotationRunsWhileAstraReuseIsOff(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, Models: []string{"gpt-6-astra"}}, nil)
	s.cfg.Gateway.CodexGatewayPin.Enabled = false
	require.False(t, s.UnifiedAstraTicketsEnabled(context.Background()))
	require.True(t, s.codexGatewayRotationEnabled(context.Background()))
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-4"))
	account := ticketTestAccount(7)
	header := http.Header{}
	require.NoError(t, s.applyOpenAICodexTicket(context.Background(), account, "gpt-5.4", header))
	require.Equal(t, "chat.gateway.unified-4.api.openai.com", codex780CookieGatewayHost(strings.Split(header.Get("Cookie"), "; ")))
	turns := s.CollectedAstraGatewayTurns()
	require.Len(t, turns, 1)
	require.Equal(t, int64(7), turns[0].AccountID)
	require.Equal(t, "chat.gateway.unified-4.api.openai.com", turns[0].Host)
}

func TestGatewayRotationCoversEveryCodexOAuthModel(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, Models: []string{"gpt-6-astra"}}, nil)
	s.accountRepo = &astraTicketRepo{accounts: map[int64]*Account{}}
	s.cfg.Gateway.CodexGatewayPin.Enabled = true
	s.rememberGatewayCookie(mint780Pair(time.Now().Add(time.Hour), "unified-9"))
	oauth := ticketTestAccount(7)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	require.NoError(t, err)
	s.overlayAccountGatewayCookie(req, oauth)
	require.Equal(t, "chat.gateway.unified-9.api.openai.com", codex780CookieGatewayHost(strings.Split(req.Header.Get("Cookie"), "; ")))
	apiKey := ticketTestAccount(8)
	apiKey.Type = AccountTypeAPIKey
	plain, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	require.NoError(t, err)
	plain.Header.Set("Cookie", "session=keep")
	s.overlayAccountGatewayCookie(plain, apiKey)
	require.Equal(t, "session=keep", plain.Header.Get("Cookie"))
}

func TestSameAccountKeepsOnlyTwoLatestGatewayCookies(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	start := time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	host := "chat.gateway.unified-125.api.openai.com"
	first := mint780Pair(start.Add(time.Hour), "unified-125")
	second := mint780Pair(start.Add(2*time.Hour), "unified-125")
	third := mint780Pair(start.Add(3*time.Hour), "unified-125")
	other := mint780Pair(start.Add(2*time.Hour), "unified-125")
	s.rememberGatewayCookieFrom(7, "rotating", first, start)
	s.rememberGatewayCookieFrom(7, "rotating", second, start.Add(time.Minute))
	s.rememberGatewayCookieFrom(8, "other", other, start)
	s.rememberGatewayCookieFrom(7, "rotating", second, start.Add(90*time.Second))
	s.rememberGatewayCookieFrom(7, "rotating", third, start.Add(2*time.Minute))
	list := s.collectedAstraGatewaysAt(start.Add(2 * time.Minute))
	require.Len(t, list, 3)
	var own []AstraGatewayPoolEntry
	for _, item := range list {
		require.Equal(t, host, item.Host)
		if item.AccountID == 7 {
			own = append(own, item)
		} else {
			require.Equal(t, int64(8), item.AccountID)
		}
	}
	require.Len(t, own, 2)
	require.ElementsMatch(t, []time.Time{start.Add(time.Minute), start.Add(2 * time.Minute)}, []time.Time{own[0].SavedAt, own[1].SavedAt})
}

func TestSameGatewayKeepsEachAccountCookieAsBackup(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(9)
	start := time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	host := "chat.gateway.unified-125.api.openai.com"
	longLived := mint780Pair(start.Add(2*time.Hour), "unified-125")
	shortLived := mint780Pair(start.Add(30*time.Second), "unified-125")
	s.rememberGatewayCookieFrom(2, "bob", longLived, start)
	s.rememberGatewayCookieFrom(1, "alice", shortLived, start)
	list := s.collectedAstraGatewaysAt(start)
	require.Len(t, list, 2)
	require.Equal(t, host, list[0].Host)
	require.Equal(t, host, list[1].Host)
	require.Equal(t, "primary", list[0].Role)
	require.Equal(t, int64(2), list[0].AccountID)
	require.Equal(t, "bob", list[0].AccountName)
	require.Equal(t, "backup", list[1].Role)
	require.Equal(t, int64(1), list[1].AccountID)
	require.Equal(t, "alice", list[1].AccountName)

	slot, switched := s.currentAccountGateway(account, "gpt-6-astra", start)
	require.True(t, switched)
	require.Equal(t, host, slot.Host)
	require.Equal(t, longLived, slot.Cookies)
	again, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(31*time.Second))
	require.False(t, switched)
	require.Equal(t, longLived, again.Cookies)
	later := s.collectedAstraGatewaysAt(start.Add(31 * time.Second))
	require.Len(t, later, 1)
	require.Equal(t, "primary", later[0].Role)
	require.Equal(t, int64(2), later[0].AccountID)
	raw, err := json.Marshal(later)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "__oailb")
	require.NotContains(t, string(raw), "__cflb")
}

func TestBusinessResponseCapturesGatewayCookie(t *testing.T) {
	resetCodexHarvestFlow()
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(4)
	account.Name = "biz"
	resp := &http.Response{StatusCode: 500, Header: http.Header{"Set-Cookie": mint780Pair(time.Now().Add(time.Hour), "unified-77")}}
	s.noteBusinessGatewayCookie(account, "gpt-5.6-sol", resp)
	s.noteBusinessGatewayCookie(account, "gpt-5.6-sol", resp)
	list := s.CollectedAstraGateways()
	require.Len(t, list, 1)
	require.Equal(t, "chat.gateway.unified-77.api.openai.com", list[0].Host)
	require.Equal(t, int64(4), list[0].AccountID)
	require.Equal(t, "biz", list[0].AccountName)
	require.Equal(t, "active", list[0].Status)
	seen := 0
	for _, event := range listCodexHarvestFlowEvents() {
		if event.Kind != "gateway_seen" {
			continue
		}
		seen++
		require.Equal(t, int64(4), event.AccountID)
		require.Equal(t, "gpt-5.6-sol", event.Model)
		require.NotContains(t, event.Detail, "__oailb")
	}
	require.Equal(t, 1, seen)
	s.noteBusinessGatewayCookie(account, "gpt-5.6-sol", &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": []string{"__cflb=route"}}})
	require.Len(t, s.CollectedAstraGateways(), 1)
}

func TestGatewayBackupReplacesTheCookieInUse(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(9)
	start := time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	current := mint780Pair(start.Add(30*time.Second), "unified-125")
	spare := mint780Pair(start.Add(2*time.Hour), "unified-125")
	s.rememberGatewayCookieFrom(1, "alice", current, start)
	slot, switched := s.currentAccountGateway(account, "gpt-6-astra", start)
	require.True(t, switched)
	require.Equal(t, current, slot.Cookies)
	s.rememberGatewayCookieFrom(2, "bob", spare, start.Add(10*time.Second))
	held, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(20*time.Second))
	require.False(t, switched)
	require.Equal(t, current, held.Cookies)
	promoted, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(31*time.Second))
	require.False(t, switched)
	require.Equal(t, "chat.gateway.unified-125.api.openai.com", promoted.Host)
	require.Equal(t, spare, promoted.Cookies)
}

func TestGatewayCredentialExpiryRetiresCookieInsideTheHour(t *testing.T) {
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(7)
	start := time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	s.rememberGatewayCookieAt(mint780Pair(start.Add(30*time.Second), "unified-125"), start)
	s.rememberGatewayCookieAt(mint780Pair(start.Add(2*time.Hour), "unified-126"), start)
	first, switched := s.currentAccountGateway(account, "gpt-6-astra", start)
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-125.api.openai.com", first.Host)
	next, switched := s.currentAccountGateway(account, "gpt-6-astra", start.Add(31*time.Second))
	require.True(t, switched)
	require.Equal(t, "chat.gateway.unified-126.api.openai.com", next.Host)
	list := s.collectedAstraGatewaysAt(start.Add(31 * time.Second))
	require.Len(t, list, 1)
	require.Equal(t, "chat.gateway.unified-126.api.openai.com", list[0].Host)
	require.Equal(t, "active", list[0].Status)
}
