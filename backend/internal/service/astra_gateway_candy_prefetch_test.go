package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCandyPrefetchCount(t *testing.T) {
	require.Equal(t, 0, gatewayCandyPrefetchCount(0, 4))
	require.Equal(t, 5, gatewayCandyPrefetchCount(20, 4))
	require.Equal(t, 1, gatewayCandyPrefetchCount(4, 4))
	require.Equal(t, 0, gatewayCandyPrefetchCount(3, 4))
}

type candyPreferSettingRepo struct {
	SettingRepository
}

func (candyPreferSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyOpenAICodexGatewayCandyPrefer {
		return "true", nil
	}
	return "", nil
}

func TestCandyPreferredAccountBeatsHigherScoreInsideTopK(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{
		settingService: &SettingService{settingRepo: candyPreferSettingRepo{}},
	}}
	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{}, openAIAccountLoadPlan{
		topK: 1,
		candidates: []openAIAccountCandidateScore{
			{account: &Account{ID: 2, Priority: 50}, score: 100},
			{account: &Account{ID: 1, Priority: 1}, score: 1, candyPreferred: true},
		},
	})
	require.NotEmpty(t, order)
	require.Equal(t, int64(1), order[0].account.ID)
}
