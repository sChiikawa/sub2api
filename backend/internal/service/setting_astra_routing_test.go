package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type astraSettingsRepo struct {
	*codexPolicyMigrationRepoStub
	failure error
}

func (r *astraSettingsRepo) Set(ctx context.Context, k, v string) error {
	if r.failure != nil {
		return r.failure
	}
	return r.codexPolicyMigrationRepoStub.Set(ctx, k, v)
}
func TestAstraGatewaySettingsPersistAndApply(t *testing.T) {
	repo := &astraSettingsRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS = config.GatewayOpenAIWSConfig{Enabled: true, OAuthEnabled: true, ResponsesWebsocketsV2: true}
	svc := NewSettingService(repo, cfg)
	initial, err := svc.GetAstraRouting(t.Context())
	require.NoError(t, err)
	require.False(t, initial.CookiePool.Enabled)
	require.False(t, initial.CookiePool.IPAffinity)
	initial.CookiePool = config.CodexGatewayPinConfig{Enabled: true, IPAffinity: true, SourceAccountIDs: []int64{299}, TargetAccountIDs: []int64{300}}
	initial.WSSession = config.CodexWSAnchorConfig{Enabled: true, AccountIDs: []int64{300}}
	saved, err := svc.SetAstraRouting(t.Context(), initial)
	require.NoError(t, err)
	require.NotEmpty(t, saved.Revision)
	require.True(t, cfg.AstraRouting(t.Context()).CookiePool.Enabled)
	require.False(t, cfg.Gateway.CodexGatewayPin.Enabled, "immutable startup config")
	cfg2 := &config.Config{}
	_ = NewSettingService(repo, cfg2)
	require.Equal(t, saved, cfg2.AstraRouting(t.Context()), "survives process initialization")
	repo.failure = errors.New("database unavailable")
	changed := saved
	changed.CookiePool.Enabled = false
	changed.WSSession.Enabled = false
	_, err = svc.SetAstraRouting(t.Context(), changed)
	require.Error(t, err)
	require.True(t, cfg.AstraRouting(t.Context()).CookiePool.Enabled, "failed save must not change runtime")
	repo.failure = nil
	disabled, err := svc.SetAstraRouting(t.Context(), changed)
	require.NoError(t, err)
	require.NotEqual(t, saved.Revision, disabled.Revision)
	require.False(t, cfg.AstraRouting(t.Context()).CookiePool.Enabled)
	svc.astraRoutingExpires = time.Time{}
	require.Equal(t, disabled, cfg.AstraRouting(t.Context()))
	changed.CookiePool.Enabled = true
	changed.CookiePool.SourceAccountIDs = nil
	_, err = svc.SetAstraRouting(t.Context(), changed)
	require.EqualError(t, err, "astra_source_required")
}

func TestAstraRoutingDerivesBorrowersAndWSMembers(t *testing.T) {
	repo := &astraSettingsRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS = config.GatewayOpenAIWSConfig{Enabled: true, OAuthEnabled: true, ResponsesWebsocketsV2: true}
	svc := NewSettingService(repo, cfg)
	oauth := func(id int64, extra map[string]any) Account {
		return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 2, Extra: extra}
	}
	ws := map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}
	accounts := []Account{
		oauth(299, ws),
		oauth(300, ws),
		oauth(301, nil),
		oauth(302, map[string]any{OpenAICodexSkipHarvestExtraKey: true}),
		{ID: 303, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 2, Extra: ws},
	}
	var listErr error
	svc.SetAstraAccountLister(func(context.Context) ([]Account, error) { return accounts, listErr })
	saved, err := svc.SetAstraRouting(t.Context(), config.AstraRoutingSettings{
		CookiePool: config.CodexGatewayPinConfig{Enabled: true, SourceAccountIDs: []int64{299}, TargetAccountIDs: []int64{302}},
		WSSession:  config.CodexWSAnchorConfig{Enabled: true, AccountIDs: []int64{303}},
	})
	require.NoError(t, err)
	require.Equal(t, []int64{300, 301}, saved.CookiePool.TargetAccountIDs, "harvest scope minus sources and opt-outs; submitted lists are ignored")
	require.Equal(t, []int64{299, 300}, saved.WSSession.AccountIDs, "route-pool accounts that already run WSv2")
	stored, err := svc.GetAstraRouting(t.Context())
	require.NoError(t, err)
	require.Nil(t, stored.CookiePool.TargetAccountIDs)
	require.Nil(t, stored.WSSession.AccountIDs)
	require.NotContains(t, repo.values[astraRoutingSettingKey], "301")

	accounts = append(accounts, oauth(304, nil))
	svc.astraRoutingExpires = time.Time{}
	runtime := cfg.AstraRouting(t.Context())
	require.Equal(t, []int64{300, 301, 304}, runtime.CookiePool.TargetAccountIDs)
	require.Equal(t, saved.Revision, runtime.Revision, "membership changes never invalidate routes or sessions")

	listErr = errors.New("database unavailable")
	svc.astraRoutingExpires = time.Time{}
	require.Equal(t, []int64{300, 301, 304}, cfg.AstraRouting(t.Context()).CookiePool.TargetAccountIDs, "a failed inventory read keeps current members")
	listErr = nil

	view, err := svc.GetAstraRoutingWithMembers(t.Context())
	require.NoError(t, err)
	require.Equal(t, []int64{300, 301, 304}, view.CookiePool.TargetAccountIDs)
	again, err := svc.SetAstraRouting(t.Context(), view)
	require.NoError(t, err)
	require.Equal(t, saved.Revision, again.Revision)
}

func TestAstraGatewaySaveTriggersSetupOnlyAfterCommit(t *testing.T) {
	repo := &astraSettingsRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	cfg := &config.Config{}
	svc := NewSettingService(repo, cfg)
	calls := 0
	svc.SetAstraRoutingOnSaved(func(v config.AstraRoutingSettings) {
		calls++
		require.Equal(t, v.Revision, cfg.AstraRouting(t.Context()).Revision)
	})
	v := config.AstraRoutingSettings{CookiePool: config.CodexGatewayPinConfig{Enabled: true, SourceAccountIDs: []int64{299}, TargetAccountIDs: []int64{300}}}
	_, err := svc.SetAstraRouting(t.Context(), v)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	repo.failure = errors.New("database failed")
	_, err = svc.SetAstraRouting(t.Context(), v)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
