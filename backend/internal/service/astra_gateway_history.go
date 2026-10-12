package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

type AstraGatewayHistoryRecord struct {
	Gateway         string     `json:"gateway"`
	SourceAccountID int64      `json:"source_account_id"`
	TargetAccountID int64      `json:"target_account_id"`
	Passes          int64      `json:"passes"`
	Failures        int64      `json:"failures"`
	FirstSeen       time.Time  `json:"first_seen"`
	LastSeen        time.Time  `json:"last_seen"`
	LastPass        *time.Time `json:"last_pass,omitempty"`
	LastFailure     *time.Time `json:"last_failure,omitempty"`
	LastAnswer      string     `json:"last_answer"`
	LastReason      string     `json:"last_reason"`
}
type AstraGatewayHistoryPage struct {
	Items          []AstraGatewayHistoryRecord `json:"items"`
	Total          int64                       `json:"total"`
	UniqueGateways int64                       `json:"unique_gateways"`
}
type AstraGatewayHistoryRepository interface {
	RecordAstraGateway(context.Context, AstraGatewayHistoryRecord, bool) error
	ListAstraGateways(context.Context, string, bool, int, int) (AstraGatewayHistoryPage, error)
}
type AstraGatewayHistoryRecorder interface {
	SetAstraGatewayHistoryRecorder(func(AstraGatewayHistoryRecord, bool))
}

// recordAstraGatewayHistory stores a gateway hostname taken from a route cookie.
// Callers must already have reduced it to the known OpenAI gateway host shape.
func (s *OpenAIGatewayService) recordAstraGatewayHistory(row AstraGatewayHistoryRecord, passed bool) {
	if s == nil || s.settingService == nil || row.Gateway == "" || row.SourceAccountID <= 0 {
		return
	}
	history, ok := s.settingService.settingRepo.(AstraGatewayHistoryRepository)
	if !ok {
		return
	}
	if row.LastSeen.IsZero() {
		row.LastSeen = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := history.RecordAstraGateway(ctx, row, passed); err != nil {
		logger.L().Warn("astra gateway history persistence failed")
	}
}

func (s *AccountTestService) AstraGatewayHistory(ctx context.Context, host string, passed bool, offset, limit int) (AstraGatewayHistoryPage, error) {
	if s.settingService != nil {
		if repo, ok := s.settingService.settingRepo.(AstraGatewayHistoryRepository); ok {
			return repo.ListAstraGateways(ctx, host, passed, offset, limit)
		}
	}
	return AstraGatewayHistoryPage{}, errors.New("gateway_history_unavailable")
}
