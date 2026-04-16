package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// response body helpers
func decodeHTTPBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer reader.Close()
		return io.ReadAll(reader)
	}

	return io.ReadAll(resp.Body)
}

// general
func extractJobIDs(query string) []string {
	match := jobIDRegex.FindStringSubmatch(query)
	if len(match) < 2 {
		return nil
	}

	parts := strings.Split(match[1], "|")
	jobIDs := make([]string, 0, len(parts))
	for _, jobID := range parts {
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			continue
		}
		jobIDs = append(jobIDs, jobID)
	}

	return jobIDs
}

func replaceJobFilter(query, jobID string) string {
	return jobIDRegex.ReplaceAllString(query, fmt.Sprintf(`job="%s"`, jobID))
}

// request helpers
func overwriteTimeframe(targetURL *url.URL, tsStart, tsEnd int64) {
	q := targetURL.Query()
	q.Set("start", fmt.Sprintf("%d", tsStart))
	q.Set("end", fmt.Sprintf("%d", tsEnd))
	targetURL.RawQuery = q.Encode()
}

// prometheus response helpers
func applyJobOffsetsAndFilter(promData *PromResponse, jobMap map[string]int64) {
	filtered := make([]Result, 0, len(promData.Data.Result))

	for i, result := range promData.Data.Result {
		jobID, ok := result.Metric["job"]
		if !ok {
			log.Printf("Error: result[%d] has no 'job' field in metric, dropping series", i)
			continue
		}

		tsStart, found := jobMap[jobID]
		if !found {
			log.Printf("Error: job %q not found in jobMap, dropping series", jobID)
			continue
		}

		for j := range result.Values {
			if len(result.Values[j]) == 0 {
				continue
			}

			rawTs := result.Values[j][0]
			var ts int64
			switch v := rawTs.(type) {
			case float64:
				ts = int64(v)
			case int64:
				ts = v
			default:
				log.Printf("Unexpected timestamp type: %T", rawTs)
				continue
			}

			result.Values[j][0] = ts - tsStart
		}

		filtered = append(filtered, result)
	}

	promData.Data.Result = filtered
}
