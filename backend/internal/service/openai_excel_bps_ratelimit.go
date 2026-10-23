package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
)

// ExcelBPSRateLimitedReason marks a BPS 429 that another account may still
// serve. It doubles as the client-facing error code once failover is exhausted.
const ExcelBPSRateLimitedReason = GatewayFailureReason("basispoints_rate_limited")

// ExcelBPSUpstreamErrorReason marks an in-band BPS failure whose code was
// normalized to basispoints_upstream_error. Another account may still serve it.
const ExcelBPSUpstreamErrorReason = GatewayFailureReason("basispoints_upstream_error")

// ExcelBPSStreamIdleReason marks a BPS stream that stalled before any semantic
// event reached the client. Another account may still serve the same request.
const ExcelBPSStreamIdleReason = GatewayFailureReason("basispoints_stream_idle")

const excelBPSRateLimitedClientMessage = "Excel BPS rate limit exceeded, please retry later"
const excelBPSUpstreamErrorClientMessage = "Excel BPS upstream request failed, please retry later"
const excelBPSStreamIdleClientMessage = "Excel BPS stream timed out, please retry later"

// excelBPSRateLimitedFilterReason names BPS cooldowns in "no available
// accounts" diagnostics; the handler reports such pools as rate limited.
const excelBPSRateLimitedFilterReason = "excel_bps_rate_limited"

// BPS throttles its own endpoint independently of the account's Codex quota.
// A BPS 429 is therefore handed back to the handler's account-switch loop
// before any response byte is written, and cools only this account's BPS
// route. Natively forwarded models, persisted quota fields and the shared
// scheduler health score are left alone. Like other runtime blocks, the
// cooldown is local to this process.
func newExcelBPSRateLimitedFailoverError(retryAfter string) *UpstreamFailoverError {
	failoverErr := &UpstreamFailoverError{
		StatusCode:        http.StatusTooManyRequests,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeAccount,
		Reason:            ExcelBPSRateLimitedReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  http.StatusTooManyRequests,
		ClientMessage:     excelBPSRateLimitedClientMessage,
	}
	// The BPS body may echo request data and is never forwarded. Only a valid
	// Retry-After survives; the handler checks it again before responding.
	if _, ok := excelBPSRetryAfter(retryAfter, time.Now()); ok {
		failoverErr.ResponseHeaders = http.Header{"Retry-After": {strings.TrimSpace(retryAfter)}}
	}
	return failoverErr
}

func newExcelBPSUpstreamFailoverError(status int) *UpstreamFailoverError {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return &UpstreamFailoverError{
		StatusCode:        status,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeAccount,
		Reason:            ExcelBPSUpstreamErrorReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  status,
		ClientMessage:     excelBPSUpstreamErrorClientMessage,
	}
}

// newExcelBPSStreamIdleFailoverError asks the handler to send the same request
// to another account. It is only returned before semantic SSE reaches the client.
func newExcelBPSStreamIdleFailoverError() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusGatewayTimeout,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeAccount,
		Reason:            ExcelBPSStreamIdleReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  http.StatusGatewayTimeout,
		ClientMessage:     excelBPSStreamIdleClientMessage,
	}
}

// excelBPSStreamIdleError matches the BPS progress deadline and an upstream
// read that reports the stream died while waiting for SSE.
func excelBPSStreamIdleError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, basispoints.ErrStreamProgressTimeout) || errors.Is(err, errOpenAISSEIdle) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "waiting for sse") || strings.Contains(msg, "disconnected before completion") {
		return true
	}
	return strings.Contains(msg, "idle timeout") && (strings.Contains(msg, "sse") || strings.Contains(msg, "stream"))
}

func excelBPSDownstreamLineIsSemantic(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:")
}

func excelBPSUpstreamAttemptStarted(c *gin.Context) bool {
	return c != nil && c.GetInt("excel_bps_upstream_attempt") > 0
}

// excelBPSIdleFailoverError switches accounts only when the stall happened
// before any semantic SSE line was written. Keepalive comments do not count.
func excelBPSIdleFailoverError(c *gin.Context, ctx context.Context, err error, semanticWritten bool) *UpstreamFailoverError {
	if semanticWritten || !excelBPSStreamIdleError(err) || IsResponseCommitted(c) || isQualityObservation(ctx) {
		return nil
	}
	return newExcelBPSStreamIdleFailoverError()
}

// excelBPSChatIdleTimeoutFailover covers the chat reader interval timer. BPS
// installs that body on the same gin context, so a pre-output stall can move
// to another account instead of ending the stream.
func excelBPSChatIdleTimeoutFailover(c *gin.Context, clientOutputStarted bool) *UpstreamFailoverError {
	if clientOutputStarted || !excelBPSUpstreamAttemptStarted(c) || IsResponseCommitted(c) {
		return nil
	}
	if c != nil && c.Request != nil && isQualityObservation(c.Request.Context()) {
		return nil
	}
	return newExcelBPSStreamIdleFailoverError()
}

func excelBPSResponseStarted(c *gin.Context) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	return IsResponseCommitted(c) || c.Writer.Written() || c.Writer.Size() > 0
}

// excelBPSShouldSwitchInBandFailure is true when BPS accepted the HTTP call
// but ended it with a 429 or an unrecognized upstream error before the client
// received any output.
func excelBPSShouldSwitchInBandFailure(c *gin.Context, ctx context.Context, result *OpenAIForwardResult, failure *basispoints.UpstreamFailure) bool {
	if failure == nil || isQualityObservation(ctx) {
		return false
	}
	if failure.Status != http.StatusTooManyRequests && failure.Code != "basispoints_upstream_error" {
		return false
	}
	if result != nil && result.FirstTokenMs != nil {
		return false
	}
	return !excelBPSResponseStarted(c)
}

// coolDownExcelBPS skips the account for BPS requests until the upstream
// Retry-After, or the configured 429 fallback when none is given. A shorter
// cooldown never replaces a longer one.
func (s *OpenAIGatewayService) coolDownExcelBPS(ctx context.Context, account *Account, retryAfter string) {
	if s == nil || account == nil {
		return
	}
	cooldown, ok := excelBPSRetryAfter(retryAfter, time.Now())
	if !ok {
		if cooldown, ok = s.excelBPS429FallbackCooldown(ctx, account); !ok {
			return
		}
	}
	// Same bounds as the configurable 429 fallback.
	if cooldown < time.Second {
		cooldown = time.Second
	}
	if limit := maxRateLimit429CooldownSeconds * time.Second; cooldown > limit {
		cooldown = limit
	}
	until := time.Now().Add(cooldown)
	for {
		current, loaded := s.excelBPSCooldownUntil.LoadOrStore(account.ID, until)
		if !loaded {
			break
		}
		if currentUntil, valid := current.(time.Time); valid && !until.After(currentUntil) {
			return
		}
		if s.excelBPSCooldownUntil.CompareAndSwap(account.ID, current, until) {
			break
		}
	}
	logger.LegacyPrintf("service.openai_excel_bps", "rate limited; skipping account for BPS requests: account_id=%d cooldown=%s", account.ID, cooldown)
}

func (s *OpenAIGatewayService) excelBPS429FallbackCooldown(ctx context.Context, account *Account) (time.Duration, bool) {
	if s.rateLimitService == nil {
		return defaultRateLimit429CooldownSeconds * time.Second, true
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	return s.rateLimitService.get429FallbackCooldown(stateCtx, account)
}

// excelBPSRetryAfter accepts delta-seconds or an HTTP date.
func excelBPSRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		return retryAt.Sub(now), true
	}
	return 0, false
}

// isExcelBPSCoolingDown reports a BPS cooldown only for requests this account
// would route through BPS.
func (s *OpenAIGatewayService) isExcelBPSCoolingDown(account *Account, requestedModel string) bool {
	if s == nil || account == nil {
		return false
	}
	value, ok := s.excelBPSCooldownUntil.Load(account.ID)
	if !ok {
		return false
	}
	if until, valid := value.(time.Time); valid && time.Now().Before(until) {
		return account.IsExcelBPSEnabledForModel(requestedModel)
	}
	s.excelBPSCooldownUntil.CompareAndDelete(account.ID, value)
	return false
}
