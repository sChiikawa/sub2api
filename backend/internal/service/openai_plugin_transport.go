package service

import (
	"net/http"
	"strings"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (result *http.Response, resultErr error) {
	var dressErr error
	request, dressErr = s.dressSharedAstraBusinessRequest(request, account)
	if dressErr != nil {
		return nil, dressErr
	}
	request = s.stickBoundCodexTicketRequest(request, account)
	s.overlayAccountGatewayCookie(request, account)
	gatewayModel := openAIRequestModel(request)
	if s.shouldRotateCodexOAuthGateway(request.Context(), account, gatewayModel) {
		request.Header.Del(openAICodexTurnStateHeader)
	}
	gatewayCookie := request.Header.Get("Cookie")
	gatewayHost := ""
	if s.shouldRotateCodexOAuthGateway(request.Context(), account, gatewayModel) {
		gatewayHost = codex780CookieGatewayHost(strings.Split(gatewayCookie, "; "))
	}
	candyHeaders := request.Header.Clone()
	boundTicket := s.codexTicketRequestBound(request, account)
	observationTicket := s.boundCodexTicket(request, account)
	observationHeaders := request.Header.Clone()
	releaseChat := func() {}
	if boundTicket {
		releaseChat = s.holdCodexTicketChat(account)
	}
	proxyURL, release, err := s.pinCodexTicketEgress(request, account, proxyURL)
	if err != nil {
		releaseChat()
		return nil, err
	}
	defer func() {
		observe := resultErr == nil && result != nil && result.StatusCode == http.StatusOK
		result, resultErr = attachCodexTicketEgressRelease(result, resultErr, func() {
			release()
			releaseChat()
			if observe {
				s.observeScheduledAstraCandy(request.Context(), observationHeaders, account, observationTicket)
				s.observeGatewayRotationCandy(request.Context(), candyHeaders, account, gatewayHost, gatewayCookie)
			}
		})
	}()
	defer func() {
		if result != nil {
			s.noteBusinessGatewayCookie(account, openAIRequestModel(request), result)
		}
		s.noteBusinessGatewayOverload(request.Context(), account, openAIRequestModel(request), result)
		if resultErr == nil {
			// Fast admission may have installed the ticket inside the upstream
			// adapter. Capture the binding before observation can revoke it.
			observedRequest := request
			if result != nil && result.Request != nil && s.UnifiedAstraTicketsEnabled(request.Context()) && s.codexTicketRequestBound(result.Request, account) {
				// Timing/fallback layers clone request headers before the routing
				// adapter installs a newly acquired bundle. Observe the sent copy.
				observedRequest = result.Request
			}
			if ticket := s.boundCodexTicket(observedRequest, account); ticket != nil {
				observationTicket = ticket
				observationHeaders = observedRequest.Header.Clone()
			}
			responseBound := boundTicket || s.codexTicketRequestBound(observedRequest, account)
			s.observeCodexTicketResponse(observedRequest, result, account)
			if responseBound && codexResponseMismatches(observedRequest, result, account) && s.settingService.CodexTicketStrictResponse(request.Context()) {
				if result.Body != nil {
					_ = result.Body.Close()
				}
				result = nil
				resultErr = ErrCodexTicketResponseRejected
			}
		}
	}()
	// Keep ticket observation/strict validation around the final response,
	// while every egress attempt retains its own plugin routing and trace.
	return s.doUpstreamWithProxyFallback(request.Context(), request, account, proxyURL)
}

// doOpenAIAccountTestUpstream sends account, Pelican, and IQ tests with the
// rotating gateway cookie and without a harvested ticket.
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
	body []byte,
) (*http.Response, error) {
	gateway := s.openaiGatewayService
	if gateway != nil {
		gateway.overlayAccountGatewayCookie(request, account)
		if gateway.shouldRotateCodexOAuthGateway(request.Context(), account, extractOpenAICodexTicketModel(body)) {
			request.Header.Del(openAICodexTurnStateHeader)
		}
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
