package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// A discovered OpenAI gateway keeps every account's route cookie that reached
// it. Each cookie stays for one hour from its own save. The request uses the
// cookie that remains valid the longest; the others stay as backups for the
// same gateway. Each account uses one still-valid gateway for 240 seconds.
// Gateways are not discovered in number order. The next turn is the smallest
// number this account has not used yet, including one that appeared after a
// higher number was already used. A used gateway is repeated only after every
// gateway saved at that moment has had a turn.
const (
	astraGatewayTurnDuration          = 240 * time.Second
	astraGatewayCookieTTL             = time.Hour
	astraGatewayCookiesPerAccountHost = 2
)

type astraGatewaySlot struct {
	Host                string
	AccountID           int64
	AccountName         string
	Cookies             []string
	SavedAt             time.Time
	ExpiresAt           time.Time
	CredentialExpiresAt time.Time
}

type astraGatewayTurn struct {
	Host      string
	CookieKey string
	Until     time.Time
	Used      map[string]struct{}
}

var astraGatewayNumberRE = regexp.MustCompile(`^chat\.gateway\.unified-([0-9]+)\.api\.openai\.com$`)

// AstraGatewayPoolEntry is one collected OpenAI gateway. Cookie values stay
// in process memory and are not part of the admin list.
type AstraGatewayPoolEntry struct {
	Host             string    `json:"host"`
	Order            int       `json:"order"`
	AccountID        int64     `json:"account_id,omitempty"`
	AccountName      string    `json:"account_name,omitempty"`
	Role             string    `json:"role,omitempty"`
	HasCookie        bool      `json:"has_cookie"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason,omitempty"`
	SavedAt          time.Time `json:"saved_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	RemainingSeconds int       `json:"remaining_seconds"`
}

// AstraGatewayTurnView is one account's place in the shared gateway rotation.
// An account appears only after one of its business requests has taken a turn.
type AstraGatewayTurnView struct {
	AccountID        int64     `json:"account_id"`
	Host             string    `json:"host"`
	Until            time.Time `json:"until"`
	RemainingSeconds int       `json:"remaining_seconds"`
	UsedHosts        []string  `json:"used_hosts"`
}

func (s *OpenAIGatewayService) CollectedAstraGatewayTurns() []AstraGatewayTurnView {
	return s.collectedAstraGatewayTurnsAt(time.Now())
}

func (s *OpenAIGatewayService) collectedAstraGatewayTurnsAt(now time.Time) []AstraGatewayTurnView {
	if s == nil {
		return []AstraGatewayTurnView{}
	}
	s.ensureAstraGatewayStore()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	out := make([]AstraGatewayTurnView, 0, len(s.astraGatewayTurns))
	for accountID, turn := range s.astraGatewayTurns {
		if accountID <= 0 || turn == nil || turn.Host == "" {
			continue
		}
		remaining := int(turn.Until.Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		used := make([]string, 0, len(turn.Used))
		for host := range turn.Used {
			if host != "" {
				used = append(used, host)
			}
		}
		slices.SortFunc(used, func(a, b string) int {
			if an, bn := gatewayHostNumber(a), gatewayHostNumber(b); an != bn {
				return an - bn
			}
			return strings.Compare(a, b)
		})
		out = append(out, AstraGatewayTurnView{
			AccountID:        accountID,
			Host:             turn.Host,
			Until:            turn.Until,
			RemainingSeconds: remaining,
			UsedHosts:        used,
		})
	}
	slices.SortFunc(out, func(a, b AstraGatewayTurnView) int {
		if a.AccountID < b.AccountID {
			return -1
		}
		if a.AccountID > b.AccountID {
			return 1
		}
		return 0
	})
	return out
}

func (s *OpenAIGatewayService) CollectedAstraGateways() []AstraGatewayPoolEntry {
	return s.collectedAstraGatewaysAt(time.Now())
}

func (s *OpenAIGatewayService) collectedAstraGatewaysAt(now time.Time) []AstraGatewayPoolEntry {
	if s == nil {
		return []AstraGatewayPoolEntry{}
	}
	s.ensureAstraGatewayStore()
	s.seedGatewayCookiesFromTickets()
	var removed []astraGatewaySlot
	defer func() { deleteAstraGatewaySlots(removed) }()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	removed = s.pruneGatewaySlotsLocked(now)
	best := s.bestActiveByHostLocked(now)
	out := make([]AstraGatewayPoolEntry, 0, len(s.astraGatewaySlots))
	for _, slot := range s.astraGatewaySlots {
		status, reason, remaining := gatewaySlotStatus(slot, now)
		role := ""
		if status == "active" {
			role = "backup"
			if chosen, ok := best[slot.Host]; ok && sameGatewayCookie(chosen, slot) {
				role = "primary"
			}
		}
		out = append(out, AstraGatewayPoolEntry{
			Host:             slot.Host,
			AccountID:        slot.AccountID,
			AccountName:      slot.AccountName,
			Role:             role,
			HasCookie:        len(slot.Cookies) > 0,
			Status:           status,
			Reason:           reason,
			SavedAt:          slot.SavedAt,
			ExpiresAt:        slot.ExpiresAt,
			RemainingSeconds: remaining,
		})
	}
	slices.SortFunc(out, func(a, b AstraGatewayPoolEntry) int {
		if an, bn := gatewayHostNumber(a.Host), gatewayHostNumber(b.Host); an != bn {
			return an - bn
		}
		if ra, rb := gatewayRoleRank(a.Role), gatewayRoleRank(b.Role); ra != rb {
			return ra - rb
		}
		if a.RemainingSeconds != b.RemainingSeconds {
			return b.RemainingSeconds - a.RemainingSeconds
		}
		if a.AccountID != b.AccountID {
			if a.AccountID < b.AccountID {
				return -1
			}
			return 1
		}
		return a.SavedAt.Compare(b.SavedAt)
	})
	for i := range out {
		out[i].Order = i + 1
	}
	return out
}

// ArchiveExpiredAstraGateways removes every gateway entry that can no longer
// be used (the one-hour save passed, or its credential was retired) from the
// pool and the database in one step. Usable entries and rotation progress are
// left alone. It returns how many entries were archived.
func (s *OpenAIGatewayService) ArchiveExpiredAstraGateways() int {
	return s.archiveExpiredAstraGatewaysAt(time.Now())
}

func (s *OpenAIGatewayService) archiveExpiredAstraGatewaysAt(now time.Time) int {
	if s == nil {
		return 0
	}
	s.ensureAstraGatewayStore()
	var removed []astraGatewaySlot
	defer func() { deleteAstraGatewaySlots(removed) }()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	for key, slot := range s.astraGatewaySlots {
		if !gatewaySlotActive(slot, now) {
			removed = append(removed, slot)
			delete(s.astraGatewaySlots, key)
		}
	}
	if len(removed) > 0 {
		logger.L().Info("astra gateway pool archived expired entries", zap.Int("count", len(removed)))
	}
	return len(removed)
}

func (s *OpenAIGatewayService) rememberGatewayCookie(cookies []string) {
	s.rememberGatewayCookieFrom(0, "", cookies, time.Now())
}

func (s *OpenAIGatewayService) rememberGatewayCookieAt(cookies []string, now time.Time) {
	s.rememberGatewayCookieFrom(0, "", cookies, now)
}

func (s *OpenAIGatewayService) rememberGatewayCookieFrom(accountID int64, accountName string, cookies []string, now time.Time) bool {
	pair, host := astraRouteCookiePair(cookies)
	if host == "" || s == nil {
		return false
	}
	if accountID < 0 {
		accountID = 0
	}
	s.ensureAstraGatewayStore()
	var removed []astraGatewaySlot
	var saved *astraGatewaySlot
	defer func() {
		if saved != nil {
			saveAstraGatewaySlot(*saved)
		}
		deleteAstraGatewaySlots(removed)
	}()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	removed = s.pruneGatewaySlotsLocked(now)
	if s.astraGatewaySlots == nil {
		s.astraGatewaySlots = map[string]astraGatewaySlot{}
	}
	key := astraGatewaySlotKey(host, accountID, pair)
	credentialExpires := oailbCredentialExpiry(pair)
	if existing, ok := s.astraGatewaySlots[key]; ok && now.Before(existing.ExpiresAt) {
		if accountName != "" {
			existing.AccountName = accountName
		}
		if existing.CredentialExpiresAt.IsZero() && !credentialExpires.IsZero() {
			existing.CredentialExpiresAt = credentialExpires
		}
		s.astraGatewaySlots[key] = existing
		return false
	}
	slot := astraGatewaySlot{
		Host:                host,
		AccountID:           accountID,
		AccountName:         accountName,
		Cookies:             pair,
		SavedAt:             now,
		ExpiresAt:           now.Add(astraGatewayCookieTTL),
		CredentialExpiresAt: credentialExpires,
	}
	s.astraGatewaySlots[key] = slot
	removed = append(removed, s.trimAccountGatewayCookiesLocked(host, accountID, key)...)
	saved = &slot
	return true
}

func (s *OpenAIGatewayService) trimAccountGatewayCookiesLocked(host string, accountID int64, keepKey string) []astraGatewaySlot {
	type savedCookie struct {
		key   string
		saved time.Time
	}
	mine := make([]savedCookie, 0, 4)
	for key, slot := range s.astraGatewaySlots {
		if slot.Host == host && slot.AccountID == accountID {
			mine = append(mine, savedCookie{key: key, saved: slot.SavedAt})
		}
	}
	if len(mine) <= astraGatewayCookiesPerAccountHost {
		return nil
	}
	slices.SortFunc(mine, func(a, b savedCookie) int {
		if compared := a.saved.Compare(b.saved); compared != 0 {
			return compared
		}
		if a.key == keepKey {
			return 1
		}
		if b.key == keepKey {
			return -1
		}
		return strings.Compare(a.key, b.key)
	})
	removed := make([]astraGatewaySlot, 0, len(mine)-astraGatewayCookiesPerAccountHost)
	for _, old := range mine[:len(mine)-astraGatewayCookiesPerAccountHost] {
		removed = append(removed, s.astraGatewaySlots[old.key])
		delete(s.astraGatewaySlots, old.key)
	}
	return removed
}

func (s *OpenAIGatewayService) seedGatewayCookiesFromTickets() {
	if s == nil {
		return
	}
	s.astraGatewayMu.Lock()
	if s.astraGatewaySeeded {
		s.astraGatewayMu.Unlock()
		return
	}
	s.astraGatewaySeeded = true
	s.astraGatewayMu.Unlock()
	type seededGatewayCookie struct {
		accountID int64
		cookies   []string
	}
	var found []seededGatewayCookie
	s.openaiCodexTickets.Range(func(_, value any) bool {
		ticket, _ := value.(*openAICodexTicket)
		if ticket == nil {
			return true
		}
		found = append(found, seededGatewayCookie{accountID: ticket.AccountID, cookies: ticket.HarvestCookies})
		if ticket.Standby != nil {
			accountID := ticket.Standby.AccountID
			if accountID == 0 {
				accountID = ticket.AccountID
			}
			found = append(found, seededGatewayCookie{accountID: accountID, cookies: ticket.Standby.HarvestCookies})
		}
		return true
	})
	for _, item := range found {
		s.rememberGatewayCookieFrom(item.accountID, "", item.cookies, time.Now())
	}
}

func (s *OpenAIGatewayService) currentAccountGateway(account *Account, model string, now time.Time) (*astraGatewaySlot, bool) {
	if s == nil || account == nil || account.ID <= 0 {
		return nil, false
	}
	s.ensureAstraGatewayStore()
	s.seedGatewayCookiesFromTickets()
	var removed []astraGatewaySlot
	var turnSave *AstraGatewayPoolTurn
	defer func() {
		deleteAstraGatewaySlots(removed)
		if turnSave != nil {
			saveAstraGatewayTurn(*turnSave)
		}
	}()
	s.astraGatewayMu.Lock()
	defer s.astraGatewayMu.Unlock()
	removed = s.pruneGatewaySlotsLocked(now)
	ordered := s.usableGatewaySlotsLocked(now)
	if len(ordered) == 0 {
		return nil, false
	}
	if s.astraGatewayTurns == nil {
		s.astraGatewayTurns = map[int64]*astraGatewayTurn{}
	}
	turn := s.astraGatewayTurns[account.ID]
	if turn != nil && turn.Host != "" && now.Before(turn.Until) {
		if slot, ok := s.astraGatewaySlots[turn.CookieKey]; ok && slot.Host == turn.Host && gatewaySlotActive(slot, now) {
			return cloneGatewaySlot(slot), false
		}
		if slot, ok := s.bestActiveByHostLocked(now)[turn.Host]; ok {
			turn.CookieKey = astraGatewaySlotKey(slot.Host, slot.AccountID, slot.Cookies)
			turnSave = gatewayTurnRecord(account.ID, turn)
			return cloneGatewaySlot(slot), false
		}
	}
	if turn == nil {
		turn = &astraGatewayTurn{Used: map[string]struct{}{}}
		s.astraGatewayTurns[account.ID] = turn
	}
	if turn.Used == nil {
		turn.Used = map[string]struct{}{}
	}
	next := firstUnusedGateway(ordered, turn.Used)
	if next == nil {
		// Every gateway saved right now has had a turn. A gateway discovered
		// after this point starts the next round; none of the current ones is skipped.
		turn.Used = map[string]struct{}{}
		next = &ordered[0]
	}
	turn.Host = next.Host
	turn.CookieKey = astraGatewaySlotKey(next.Host, next.AccountID, next.Cookies)
	turn.Until = now.Add(astraGatewayTurnDuration)
	turn.Used[next.Host] = struct{}{}
	turnSave = gatewayTurnRecord(account.ID, turn)
	recordCodexHarvestFlow(CodexHarvestFlowEvent{
		Stage:       "probe",
		Kind:        "gateway_turn",
		AccountID:   account.ID,
		AccountName: account.Name,
		Model:       model,
		Node:        next.Host,
		Result:      "gateway_turn",
		Detail:      next.Host,
	})
	return cloneGatewaySlot(*next), true
}

func (s *OpenAIGatewayService) pruneGatewaySlotsLocked(now time.Time) []astraGatewaySlot {
	var removed []astraGatewaySlot
	for key, slot := range s.astraGatewaySlots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt.Add(astraGatewayCookieTTL)) {
			removed = append(removed, slot)
			delete(s.astraGatewaySlots, key)
		}
	}
	return removed
}

func (s *OpenAIGatewayService) usableGatewaySlotsLocked(now time.Time) []astraGatewaySlot {
	best := s.bestActiveByHostLocked(now)
	usable := make([]astraGatewaySlot, 0, len(best))
	for _, slot := range best {
		usable = append(usable, slot)
	}
	slices.SortFunc(usable, func(a, b astraGatewaySlot) int {
		if an, bn := gatewayHostNumber(a.Host), gatewayHostNumber(b.Host); an != bn {
			return an - bn
		}
		return strings.Compare(a.Host, b.Host)
	})
	return usable
}

func (s *OpenAIGatewayService) bestActiveByHostLocked(now time.Time) map[string]astraGatewaySlot {
	best := map[string]astraGatewaySlot{}
	for _, slot := range s.astraGatewaySlots {
		if !gatewaySlotActive(slot, now) {
			continue
		}
		current, ok := best[slot.Host]
		if !ok || preferGatewayCookie(slot, current) {
			best[slot.Host] = slot
		}
	}
	return best
}

func preferGatewayCookie(candidate, current astraGatewaySlot) bool {
	untilCandidate := gatewayCookieUsableUntil(candidate)
	untilCurrent := gatewayCookieUsableUntil(current)
	if !untilCandidate.Equal(untilCurrent) {
		return untilCandidate.After(untilCurrent)
	}
	if candidate.AccountID != current.AccountID {
		return candidate.AccountID < current.AccountID
	}
	return candidate.SavedAt.Before(current.SavedAt)
}

func gatewayCookieUsableUntil(slot astraGatewaySlot) time.Time {
	until := slot.ExpiresAt
	if !slot.CredentialExpiresAt.IsZero() && slot.CredentialExpiresAt.Before(until) {
		until = slot.CredentialExpiresAt
	}
	return until
}

func sameGatewayCookie(a, b astraGatewaySlot) bool {
	return a.Host == b.Host && a.AccountID == b.AccountID && slices.Equal(a.Cookies, b.Cookies)
}

func gatewayRoleRank(role string) int {
	switch role {
	case "primary":
		return 0
	case "backup":
		return 1
	default:
		return 2
	}
}

func astraGatewaySlotKey(host string, accountID int64, cookies []string) string {
	return host + "\x00" + strconv.FormatInt(accountID, 10) + "\x00" + strings.Join(cookies, "\n")
}

func gatewaySlotActive(slot astraGatewaySlot, now time.Time) bool {
	status, _, _ := gatewaySlotStatus(slot, now)
	return status == "active"
}

func gatewaySlotStatus(slot astraGatewaySlot, now time.Time) (status, reason string, remaining int) {
	if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
		return "expired", "", 0
	}
	remaining = int(slot.ExpiresAt.Sub(now).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	if !slot.CredentialExpiresAt.IsZero() && !now.Before(slot.CredentialExpiresAt) {
		return "retired", "credential_expired", remaining
	}
	return "active", "", remaining
}

func oailbCredentialExpiry(cookies []string) time.Time {
	for _, cookie := range cookies {
		name, value, ok := strings.Cut(cookie, "=")
		if !ok || name != "__oailb" {
			continue
		}
		parts := strings.Split(value, ".")
		if len(parts) != 3 {
			return time.Time{}
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
			return time.Time{}
		}
		return time.Unix(claims.Exp, 0)
	}
	return time.Time{}
}

// firstUnusedGateway returns the smallest numbered gateway that has not had a
// turn in this round. Missing numbers and late discoveries stay in the list;
// only a host already used is passed over.
func firstUnusedGateway(ordered []astraGatewaySlot, used map[string]struct{}) *astraGatewaySlot {
	for i := range ordered {
		if _, seen := used[ordered[i].Host]; !seen {
			return &ordered[i]
		}
	}
	return nil
}

func gatewayHostNumber(host string) int {
	match := astraGatewayNumberRE.FindStringSubmatch(host)
	if len(match) != 2 {
		return 1 << 30
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return 1 << 30
	}
	return n
}

func cloneGatewaySlot(slot astraGatewaySlot) *astraGatewaySlot {
	slot.Cookies = append([]string(nil), slot.Cookies...)
	return &slot
}

func astraRouteCookiePair(cookies []string) ([]string, string) {
	host := codex780CookieGatewayHost(cookies)
	if host == "" {
		return nil, ""
	}
	var cflb, oailb string
	for _, cookie := range cookies {
		name, value, ok := strings.Cut(cookie, "=")
		if !ok || value == "" {
			continue
		}
		switch name {
		case "__cflb":
			if cflb == "" {
				cflb = value
			}
		case "__oailb":
			if oailb == "" {
				oailb = value
			}
		}
	}
	if oailb == "" {
		return nil, ""
	}
	pair := []string{"__oailb=" + oailb}
	if cflb != "" {
		pair = []string{"__cflb=" + cflb, "__oailb=" + oailb}
	}
	return pair, host
}

func (s *OpenAIGatewayService) shouldRotateCodexOAuthGateway(ctx context.Context, account *Account, model string) bool {
	if s == nil || account == nil || !account.IsOpenAIOAuth() || account.IsShadow() || !s.UnifiedAstraTicketsEnabled(ctx) {
		return false
	}
	if account.isExcelBPSAllModelsEnabled() || account.isExcelBPSUpstreamModelEnabled(model) {
		return false
	}
	if IsAstraSourceAcquisition(ctx) || IsOpenAICodexStateProbeRequest(ctx) {
		return false
	}
	return true
}

func (s *OpenAIGatewayService) overlayAccountGatewayCookie(req *http.Request, account *Account) {
	if s == nil || req == nil || account == nil {
		return
	}
	raw, _ := snapshotHTTPRequestBody(req)
	if req.GetBody == nil && len(raw) > 0 {
		replaceHTTPRequestJSONBody(req, raw)
	}
	model := extractOpenAICodexTicketModel(raw)
	if !s.shouldRotateCodexOAuthGateway(req.Context(), account, model) {
		return
	}
	slot, _ := s.currentAccountGateway(account, model, time.Now())
	if slot == nil {
		return
	}
	req.Header.Set("Cookie", strings.Join(slot.Cookies, "; "))
}
