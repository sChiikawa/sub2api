package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// AstraGatewayPoolSlot is one saved gateway cookie. Cookie values stay in the
// database and are not returned by the admin list.
type AstraGatewayPoolSlot struct {
	Host                string
	AccountID           int64
	AccountName         string
	Cookies             []string
	SavedAt             time.Time
	ExpiresAt           time.Time
	CredentialExpiresAt time.Time
}

// AstraGatewayPoolTurn is one account's place in the gateway rotation.
type AstraGatewayPoolTurn struct {
	AccountID int64
	Host      string
	CookieKey string
	Until     time.Time
	UsedHosts []string
}

// AstraGatewayPoolRepository stores the gateway pool and rotation progress
// across process restarts.
type AstraGatewayPoolRepository interface {
	ListSlots(context.Context) ([]AstraGatewayPoolSlot, error)
	SaveSlot(context.Context, AstraGatewayPoolSlot) error
	DeleteSlot(context.Context, AstraGatewayPoolSlot) error
	ListTurns(context.Context) ([]AstraGatewayPoolTurn, error)
	SaveTurn(context.Context, AstraGatewayPoolTurn) error
}

var astraGatewayPool atomic.Pointer[AstraGatewayPoolRepository]

// BindAstraGatewayPoolRepository attaches the database store. A nil repository
// keeps the pool in memory only.
func BindAstraGatewayPoolRepository(repo AstraGatewayPoolRepository) {
	if repo == nil {
		astraGatewayPool.Store(nil)
		return
	}
	astraGatewayPool.Store(&repo)
}

func astraGatewayPoolStore() AstraGatewayPoolRepository {
	repo := astraGatewayPool.Load()
	if repo == nil {
		return nil
	}
	return *repo
}

func (s *OpenAIGatewayService) ensureAstraGatewayStore() {
	if s == nil {
		return
	}
	s.astraGatewayMu.Lock()
	if s.astraGatewayLoaded {
		s.astraGatewayMu.Unlock()
		return
	}
	s.astraGatewayLoaded = true
	s.astraGatewayMu.Unlock()
	slots, turns := loadAstraGatewayPool()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	if s.astraGatewaySlots == nil {
		s.astraGatewaySlots = map[string]astraGatewaySlot{}
	}
	for _, item := range slots {
		slot := astraGatewaySlot{
			Host: item.Host, AccountID: item.AccountID, AccountName: item.AccountName,
			Cookies: append([]string(nil), item.Cookies...), SavedAt: item.SavedAt,
			ExpiresAt: item.ExpiresAt, CredentialExpiresAt: item.CredentialExpiresAt,
		}
		key := astraGatewaySlotKey(slot.Host, slot.AccountID, slot.Cookies)
		if _, ok := s.astraGatewaySlots[key]; !ok {
			s.astraGatewaySlots[key] = slot
		}
	}
	if s.astraGatewayTurns == nil {
		s.astraGatewayTurns = map[int64]*astraGatewayTurn{}
	}
	for _, item := range turns {
		if _, ok := s.astraGatewayTurns[item.AccountID]; ok {
			continue
		}
		used := map[string]struct{}{}
		for _, host := range item.UsedHosts {
			if host != "" {
				used[host] = struct{}{}
			}
		}
		s.astraGatewayTurns[item.AccountID] = &astraGatewayTurn{Host: item.Host, CookieKey: item.CookieKey, Until: item.Until, Used: used}
	}
}

func loadAstraGatewayPool() ([]AstraGatewayPoolSlot, []AstraGatewayPoolTurn) {
	repo := astraGatewayPoolStore()
	if repo == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	slots, err := repo.ListSlots(ctx)
	if err != nil {
		logger.L().Warn("astra gateway pool load failed", zap.Error(err))
		slots = nil
	}
	turns, err := repo.ListTurns(ctx)
	if err != nil {
		logger.L().Warn("astra gateway turn load failed", zap.Error(err))
		turns = nil
	}
	return slots, turns
}

func saveAstraGatewaySlot(slot astraGatewaySlot) {
	repo := astraGatewayPoolStore()
	if repo == nil || slot.Host == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := repo.SaveSlot(ctx, AstraGatewayPoolSlot{
		Host: slot.Host, AccountID: slot.AccountID, AccountName: slot.AccountName,
		Cookies: append([]string(nil), slot.Cookies...), SavedAt: slot.SavedAt,
		ExpiresAt: slot.ExpiresAt, CredentialExpiresAt: slot.CredentialExpiresAt,
	})
	if err != nil {
		logger.L().Warn("astra gateway pool save failed", zap.Error(err))
	}
}

func deleteAstraGatewaySlots(slots []astraGatewaySlot) {
	repo := astraGatewayPoolStore()
	if repo == nil || len(slots) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	for _, slot := range slots {
		if err := repo.DeleteSlot(ctx, AstraGatewayPoolSlot{Host: slot.Host, AccountID: slot.AccountID, Cookies: slot.Cookies}); err != nil {
			logger.L().Warn("astra gateway pool delete failed", zap.Error(err))
		}
	}
}

func saveAstraGatewayTurn(turn AstraGatewayPoolTurn) {
	repo := astraGatewayPoolStore()
	if repo == nil || turn.AccountID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := repo.SaveTurn(ctx, turn); err != nil {
		logger.L().Warn("astra gateway turn save failed", zap.Error(err))
	}
}

func gatewayTurnRecord(accountID int64, turn *astraGatewayTurn) *AstraGatewayPoolTurn {
	if turn == nil || accountID <= 0 {
		return nil
	}
	used := make([]string, 0, len(turn.Used))
	for host := range turn.Used {
		used = append(used, host)
	}
	return &AstraGatewayPoolTurn{AccountID: accountID, Host: turn.Host, CookieKey: turn.CookieKey, Until: turn.Until, UsedHosts: used}
}
