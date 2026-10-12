//go:build integration

package repository

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAstraAutomaticAtomicSettings(t *testing.T) {
	ctx := t.Context()
	client := testEntClient(t)
	source := mustCreateAccount(t, client, &service.Account{Name: "astra-donor", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Concurrency: 4, Schedulable: true, Credentials: map[string]any{"access_token": "keep-test-token", "refresh_token": "keep-refresh", "model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}}, Extra: map[string]any{"privacy_mode": "training_off", "unrelated": true}})
	t.Cleanup(func() { _ = client.Account.DeleteOneID(source.ID).Exec(ctx) })
	borrower := mustCreateAccount(t, client, &service.Account{Name: "astra-borrower", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Concurrency: 2, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}}, Extra: map[string]any{"unrelated": "kept"}})
	t.Cleanup(func() { _ = client.Account.DeleteOneID(borrower.ID).Exec(ctx) })
	key := fmt.Sprintf("astra-auto-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx) })
	repo := &settingRepository{client: client}
	value := config.AstraRoutingSettings{CookiePool: config.CodexGatewayPinConfig{Enabled: true, SourceAccountIDs: []int64{source.ID}, TargetAccountIDs: []int64{borrower.ID}}, WSSession: config.CodexWSAnchorConfig{Enabled: true, AccountIDs: []int64{borrower.ID}}, Revision: "one"}
	require.NoError(t, repo.SetAstraRoutingWithAccounts(ctx, key, value))
	got, err := client.Account.Get(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, "keep-test-token", got.Credentials["access_token"])
	require.Equal(t, "keep-refresh", got.Credentials["refresh_token"])
	mapping := got.Credentials["model_mapping"].(map[string]any)
	require.Equal(t, "gpt-6-astra", mapping["gpt-6-astra"])
	require.Equal(t, "gpt-5.6-sol", mapping["gpt-5.6-sol"])
	require.Equal(t, true, got.Extra["unrelated"])
	require.NotContains(t, got.Extra, "openai_oauth_responses_websockets_v2_enabled")
	other, err := client.Account.Get(ctx, borrower.ID)
	require.NoError(t, err)
	require.NotContains(t, other.Credentials["model_mapping"], "gpt-6-astra", "derived borrowers are not modified")
	require.NotContains(t, other.Extra, "openai_oauth_responses_websockets_v2_enabled", "WS transport stays an account decision")
	before, err := repo.GetValue(ctx, key)
	require.NoError(t, err)
	var stored config.AstraRoutingSettings
	require.NoError(t, json.Unmarshal([]byte(before), &stored))
	require.Nil(t, stored.CookiePool.TargetAccountIDs)
	require.Nil(t, stored.WSSession.AccountIDs)
	// A later missing row rolls back earlier account updates and the setting.
	require.NoError(t, client.Account.UpdateOneID(source.ID).SetExtra(map[string]any{"marker": "must-survive"}).Exec(ctx))
	beforeAccount, err := client.Account.Get(ctx, source.ID)
	require.NoError(t, err)
	value.CookiePool.SourceAccountIDs = append(value.CookiePool.SourceAccountIDs, 9223372036854775806)
	value.Revision = "two"
	require.Error(t, repo.SetAstraRoutingWithAccounts(ctx, key, value))
	after, err := repo.GetValue(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before, after)
	got, err = client.Account.Get(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, beforeAccount.Extra, got.Extra)
}
