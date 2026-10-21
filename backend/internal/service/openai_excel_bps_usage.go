package service

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// excelBPSDownstreamUsage applies the account's cache-creation-as-input policy
// to client-visible usage after recording the original upstream measurement.
// OpenAI total input already includes cache creation: keep totals and cache
// reads intact, and clear every supported cache-write alias and TTL breakdown.
func excelBPSDownstreamUsage(payload []byte) ([]byte, error) {
	for _, path := range []string{"usage", "response.usage"} {
		usage := gjson.GetBytes(payload, path)
		if !usage.IsObject() {
			continue
		}
		normalized := []byte(usage.Raw)
		changed := false
		for _, field := range []string{
			"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens",
			"input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens",
			"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens",
			"cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens",
		} {
			if value := usage.Get(field); !value.Exists() || value.Raw == "0" {
				continue
			}
			var err error
			normalized, err = sjson.SetBytes(normalized, field, 0)
			if err != nil {
				return nil, err
			}
			changed = true
		}
		if changed {
			var err error
			payload, err = sjson.SetRawBytes(payload, path, normalized)
			if err != nil {
				return nil, err
			}
		}
	}
	return payload, nil
}

// excelBPSMissedCacheReadTokens is the cache-read count shown to the client and
// billed when a BPS response reports no cached input. Image tokens and a real
// upstream cache read stay unchanged. foldCacheCreation keeps cache-creation
// tokens inside the input pool, matching cache-creation-as-input billing.
func excelBPSMissedCacheReadTokens(requestID string, usage OpenAIUsage, foldCacheCreation bool) int {
	if usage.CacheReadInputTokens > 0 {
		return 0
	}
	cacheCreation := usage.CacheCreationInputTokens
	if foldCacheCreation {
		cacheCreation = 0
	}
	actual := usage.InputTokens - usage.CacheReadInputTokens - cacheCreation
	if actual < 0 {
		actual = 0
	}
	image := usage.ImageInputTokens - usage.ImageCacheReadTokens
	if image < 0 {
		image = 0
	}
	if image > actual {
		image = actual
	}
	text := actual - image
	if text <= 0 {
		return 0
	}
	simulated := excelBPSSimulatedCacheReadTokens(requestID, text)
	if simulated > text {
		return text
	}
	return simulated
}

// excelBPSApplySimulatedCacheUsage writes the billed cache-read count into usage
// objects the client will see. input_tokens stays the upstream total, so cached
// tokens remain a subset. Objects that already report a cache read are kept.
func excelBPSApplySimulatedCacheUsage(payload []byte, cached int) ([]byte, error) {
	if cached <= 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}
	for _, path := range []string{"usage", "response.usage"} {
		usage := gjson.GetBytes(payload, path)
		if !usage.IsObject() || openAICacheReadTokensFromUsage(usage) > 0 {
			continue
		}
		var err error
		payload, err = sjson.SetBytes(payload, path+".input_tokens_details.cached_tokens", cached)
		if err != nil {
			return nil, err
		}
		payload, err = sjson.SetBytes(payload, path+".prompt_tokens_details.cached_tokens", cached)
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// excelBPSSimulatedCacheReadTokens is the cache-read share used when BPS reports
// no cached input. The share is a decimal ratio from 95.01% to 95.99% and stays
// stable for the same request id and token count. The returned token count is
// the integer part of that product; fractional tokens are dropped.
func excelBPSSimulatedCacheReadTokens(requestID string, textTokens int) int {
	if textTokens <= 0 {
		return 0
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(requestID))
	_, _ = hasher.Write([]byte{0})
	var count [8]byte
	binary.LittleEndian.PutUint64(count[:], uint64(textTokens))
	_, _ = hasher.Write(count[:])
	// 9501..9599 is 95.01%..95.99% in hundredths of a percent.
	hundredths := 9501 + int(hasher.Sum32()%99)
	return textTokens * hundredths / 10000
}
