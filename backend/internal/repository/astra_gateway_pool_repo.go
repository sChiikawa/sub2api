package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type astraGatewayPoolRepository struct{ db *sql.DB }

func newAstraGatewayPoolRepository(db *sql.DB) service.AstraGatewayPoolRepository {
	return &astraGatewayPoolRepository{db: db}
}

func (r *astraGatewayPoolRepository) ListSlots(ctx context.Context) ([]service.AstraGatewayPoolSlot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("gateway pool repository unavailable")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT host, account_id, account_name, cookies, saved_at, expires_at, credential_expires_at
FROM astra_gateway_pool`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.AstraGatewayPoolSlot
	for rows.Next() {
		var slot service.AstraGatewayPoolSlot
		var cookies string
		var credential sql.NullTime
		if err := rows.Scan(&slot.Host, &slot.AccountID, &slot.AccountName, &cookies, &slot.SavedAt, &slot.ExpiresAt, &credential); err != nil {
			return nil, err
		}
		slot.Cookies = splitStoredGatewayCookies(cookies)
		if credential.Valid {
			slot.CredentialExpiresAt = credential.Time
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}

func (r *astraGatewayPoolRepository) SaveSlot(ctx context.Context, slot service.AstraGatewayPoolSlot) error {
	if r == nil || r.db == nil {
		return errors.New("gateway pool repository unavailable")
	}
	var credential any
	if !slot.CredentialExpiresAt.IsZero() {
		credential = slot.CredentialExpiresAt
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO astra_gateway_pool
(host, account_id, account_name, cookies, saved_at, expires_at, credential_expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (host, account_id, cookies) DO UPDATE SET
account_name = EXCLUDED.account_name,
saved_at = EXCLUDED.saved_at,
expires_at = EXCLUDED.expires_at,
credential_expires_at = EXCLUDED.credential_expires_at`,
		slot.Host, slot.AccountID, slot.AccountName, joinStoredGatewayCookies(slot.Cookies), slot.SavedAt, slot.ExpiresAt, credential)
	return err
}

func (r *astraGatewayPoolRepository) DeleteSlot(ctx context.Context, slot service.AstraGatewayPoolSlot) error {
	if r == nil || r.db == nil {
		return errors.New("gateway pool repository unavailable")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM astra_gateway_pool WHERE host = $1 AND account_id = $2 AND cookies = $3`,
		slot.Host, slot.AccountID, joinStoredGatewayCookies(slot.Cookies))
	return err
}

func (r *astraGatewayPoolRepository) ListTurns(ctx context.Context) ([]service.AstraGatewayPoolTurn, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("gateway pool repository unavailable")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id, host, cookie_key, until_at, used_hosts FROM astra_gateway_turns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.AstraGatewayPoolTurn
	for rows.Next() {
		var turn service.AstraGatewayPoolTurn
		var used string
		if err := rows.Scan(&turn.AccountID, &turn.Host, &turn.CookieKey, &turn.Until, &used); err != nil {
			return nil, err
		}
		if used != "" {
			turn.UsedHosts = strings.Split(used, "\n")
		}
		out = append(out, turn)
	}
	return out, rows.Err()
}

func (r *astraGatewayPoolRepository) SaveTurn(ctx context.Context, turn service.AstraGatewayPoolTurn) error {
	if r == nil || r.db == nil {
		return errors.New("gateway pool repository unavailable")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO astra_gateway_turns (account_id, host, cookie_key, until_at, used_hosts)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (account_id) DO UPDATE SET
host = EXCLUDED.host,
cookie_key = EXCLUDED.cookie_key,
until_at = EXCLUDED.until_at,
used_hosts = EXCLUDED.used_hosts`,
		turn.AccountID, turn.Host, turn.CookieKey, turn.Until, strings.Join(turn.UsedHosts, "\n"))
	return err
}

func (r *astraGatewayPoolRepository) ListCandyPasses(ctx context.Context) ([]service.AstraGatewayCandyAccountRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("gateway pool repository unavailable")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id, host, expires_at, passed FROM astra_gateway_candy_pass`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.AstraGatewayCandyAccountRecord
	for rows.Next() {
		var pass service.AstraGatewayCandyAccountRecord
		if err := rows.Scan(&pass.AccountID, &pass.Host, &pass.Expires, &pass.Passed); err != nil {
			return nil, err
		}
		out = append(out, pass)
	}
	return out, rows.Err()
}

func (r *astraGatewayPoolRepository) SaveCandyPass(ctx context.Context, accountID int64, host string, expires time.Time, passed bool) error {
	if r == nil || r.db == nil {
		return errors.New("gateway pool repository unavailable")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO astra_gateway_candy_pass (account_id, host, expires_at, passed)
VALUES ($1,$2,$3,$4)
ON CONFLICT (account_id, host) DO UPDATE SET expires_at = EXCLUDED.expires_at, passed = EXCLUDED.passed`, accountID, host, expires, passed)
	return err
}

func (r *astraGatewayPoolRepository) DeleteCandyPass(ctx context.Context, accountID int64, host string) error {
	if r == nil || r.db == nil {
		return errors.New("gateway pool repository unavailable")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM astra_gateway_candy_pass WHERE account_id = $1 AND host = $2`, accountID, host)
	return err
}

func joinStoredGatewayCookies(cookies []string) string {
	return strings.Join(cookies, "\n")
}

func splitStoredGatewayCookies(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}
