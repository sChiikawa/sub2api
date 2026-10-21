package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const excelBPSUsageWithCreation = `{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":200,"cache_creation_tokens":200},"prompt_tokens_details":{"cached_tokens":100,"cache_write_tokens":200,"cache_creation_tokens":200},"cache_creation_input_tokens":200,"cache_write_input_tokens":200,"cache_creation_tokens":200,"cache_write_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":150,"ephemeral_1h_input_tokens":50},"output_tokens_details":{"reasoning_tokens":3},"extension":9007199254740993}`

func TestExcelBPSCacheCreationAsInputDownstreamUsage(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, stream := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/enabled=%t", path, stream, enabled), func(t *testing.T) {
					wire := `data: {"type":"response.completed","response":{"id":"resp_bps_usage","status":"completed","model":"gpt-6-astra","output":[],"usage":` + excelBPSUsageWithCreation + `}}` + "\n\n"
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
					svc := openAIClientToolsTestService(upstream)
					account := excelAccount()
					account.Extra["openai_excel_bps_cache_creation_as_input"] = enabled
					body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"usage regression"}`, stream))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					result, err := svc.Forward(context.Background(), c, account, body)
					require.NoError(t, err)
					require.Equal(t, 200, result.Usage.CacheCreationInputTokens, "retain the upstream measurement for local billing")
					response := rec.Body.Bytes()
					if stream {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if strings.HasPrefix(line, "data: ") {
								response = []byte(gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw)
							}
						}
					}
					downstream, ok := extractOpenAIUsageFromJSONBytes(response)
					require.True(t, ok)
					wantCreation, wantInput := 200, 700
					if enabled {
						wantCreation, wantInput = 0, 900
					}
					require.Equal(t, wantCreation, downstream.CacheCreationInputTokens)
					require.Equal(t, wantInput, downstream.InputTokens-downstream.CacheReadInputTokens-downstream.CacheCreationInputTokens)
					require.Equal(t, 1000, downstream.InputTokens)
					require.Equal(t, 100, downstream.CacheReadInputTokens)
					require.Equal(t, 50, downstream.OutputTokens)
					usage := gjson.GetBytes(response, "usage")
					require.EqualValues(t, 1050, usage.Get("total_tokens").Int())
					require.Equal(t, "9007199254740993", usage.Get("extension").Raw)
					require.EqualValues(t, 3, usage.Get("output_tokens_details.reasoning_tokens").Int())
					for _, field := range []string{"input_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens", "cache_write_tokens"} {
						require.EqualValues(t, wantCreation, usage.Get(field).Int(), field)
					}
					if enabled {
						require.Zero(t, usage.Get("cache_creation.ephemeral_5m_input_tokens").Int())
						require.Zero(t, usage.Get("cache_creation.ephemeral_1h_input_tokens").Int())
					} else {
						require.JSONEq(t, excelBPSUsageWithCreation, usage.Raw)
					}
				})
			}
		}
	}
}

func TestExcelBPSCacheCreationAsInputEveryStreamUsage(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete", "response.failed"} {
		t.Run(terminal, func(t *testing.T) {
			var wire strings.Builder
			for _, kind := range []string{"response.created", "response.in_progress", terminal} {
				fmt.Fprintf(&wire, "data: {\"type\":%q,\"usage\":%s,\"response\":{\"id\":\"resp_usage\",\"output\":[],\"usage\":%s}}\n\n", kind, excelBPSUsageWithCreation, excelBPSUsageWithCreation)
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire.String()))}}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			account.Extra["openai_excel_bps_cache_creation_as_input"] = true
			body := []byte(`{"model":"gpt-6-astra","stream":true,"input":"usage regression"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			result, err := svc.Forward(context.Background(), c, account, body)
			if terminal == "response.completed" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, 200, result.Usage.CacheCreationInputTokens)
			events := 0
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				events++
				payload := strings.TrimPrefix(line, "data: ")
				for _, path := range []string{"usage", "response.usage"} {
					usage := gjson.Get(payload, path)
					require.True(t, usage.IsObject())
					require.Zero(t, openAICacheCreationTokensFromUsage(usage))
					require.Zero(t, usage.Get("cache_creation_input_tokens").Int())
					require.Equal(t, 100, openAICacheReadTokensFromUsage(usage))
					require.EqualValues(t, 1000, usage.Get("input_tokens").Int())
				}
			}
			require.Equal(t, 3, events)
		})
	}
}

func TestExcelBPSMissedCacheVisibleInResponse(t *testing.T) {
	usage := `{"input_tokens":10000,"output_tokens":50,"input_tokens_details":{"cached_tokens":0,"image_tokens":400}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			wire := `data: {"type":"response.completed","usage":` + usage + `,"response":{"id":"resp_bps_cache","status":"completed","model":"gpt-6-astra","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":` + usage + `}}` + "\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "x-request-id": {"req-bps-cache"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"show the cache"}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			result, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.NoError(t, err)
			require.Equal(t, "req-bps-cache", result.RequestID)
			require.Equal(t, 10000, result.Usage.InputTokens)
			require.Zero(t, result.Usage.CacheReadInputTokens)
			want := excelBPSMissedCacheReadTokens(result.RequestID, result.Usage, false)
			require.GreaterOrEqual(t, want, 9600*9501/10000)
			require.LessOrEqual(t, want, 9600*9599/10000)
			response := rec.Body.Bytes()
			if stream {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: ") {
						response = []byte(strings.TrimPrefix(line, "data: "))
					}
				}
				for _, path := range []string{"usage", "response.usage"} {
					require.EqualValues(t, want, gjson.GetBytes(response, path+".input_tokens_details.cached_tokens").Int(), path)
					require.EqualValues(t, want, gjson.GetBytes(response, path+".prompt_tokens_details.cached_tokens").Int(), path)
					require.EqualValues(t, 10000, gjson.GetBytes(response, path+".input_tokens").Int(), path)
					require.EqualValues(t, 400, gjson.GetBytes(response, path+".input_tokens_details.image_tokens").Int(), path)
				}
			}
			downstream, ok := extractOpenAIUsageFromJSONBytes(response)
			require.True(t, ok)
			require.Equal(t, want, downstream.CacheReadInputTokens)
			require.Equal(t, 10000, downstream.InputTokens)
		})
	}
}

func TestExcelBPSApplySimulatedCacheUsageLeavesRealCacheAndDeltas(t *testing.T) {
	delta := `{"type":"response.output_text.delta","delta":"cache"}`
	got, err := excelBPSApplySimulatedCacheUsage([]byte(delta), 95)
	require.NoError(t, err)
	require.Equal(t, delta, string(got))
	real := `{"response":{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2}}}}`
	got, err = excelBPSApplySimulatedCacheUsage([]byte(real), 95)
	require.NoError(t, err)
	require.Equal(t, real, string(got))
	require.Zero(t, excelBPSMissedCacheReadTokens("req", OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 40}, false))
}

func TestExcelBPSDownstreamUsagePreservesUnrelatedPayloads(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.output_text.delta","delta":"cache_write_tokens"}`,
		`{"type":"response.completed","response":{"usage":null}}`,
		`{"response":{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2}}}}`,
		`{"response":{"usage":{"input_tokens":10,"cache_creation_input_tokens":0}}}`,
	} {
		got, err := excelBPSDownstreamUsage([]byte(payload))
		require.NoError(t, err)
		require.Equal(t, payload, string(got))
	}
}
