package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

const (
	tsStartKey contextKey = "ts_start"
	jobIDsKey  contextKey = "job_ids"
)

var (
	// Matches job="uuid" or job=~"uuid1|uuid2|..." (up to 6 UUIDs)
	jobIDRegex = regexp.MustCompile(`job=~?"([0-9a-fA-F\-]{36}(?:\|[0-9a-fA-F\-]{36}){0,5})"`)
	Cb         *CouchbaseClient
)

func NewProxyServer(targetURL string) (*ProxyServer, error) {
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	ps := &ProxyServer{
		target:  target,
		Verbose: true,
	}

	ps.proxy = httputil.NewSingleHostReverseProxy(target)

	originalDirector := ps.proxy.Director
	ps.proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		ps.inspectRequest(req)
	}

	ps.proxy.Transport = &splitQueryTransport{
		base: http.DefaultTransport,
		ps:   ps,
	}

	ps.proxy.ModifyResponse = ps.modifyResponse

	return ps, nil
}

func (ps *ProxyServer) fetchFromCouchbase(jobID string) (int64, int64, error) {
	log.Printf("implement this for %s", jobID)
	return Cb.GetTimeframe(jobID)
}

func (ps *ProxyServer) inspectRequest(req *http.Request) {
	log.Printf("inspectRequest: %s %s", req.Method, req.URL.String())
	if req.Method != http.MethodGet {
		return
	}

	query := req.URL.Query().Get("query")
	jobIDs := extractJobIDs(query)
	if len(jobIDs) == 0 {
		return
	}

	if len(jobIDs) > 1 {
		ctx := context.WithValue(req.Context(), jobIDsKey, jobIDs)
		*req = *req.WithContext(ctx)
		return
	}

	jobMap := make(map[string]int64)
	jobID := jobIDs[0]
	tsStart, tsEnd, err := ps.fetchFromCouchbase(jobID)
	if err != nil {
		log.Printf("Error fetching from Couchbase for %s: %v", jobID, err)
		return
	}
	overwriteTimeframe(req.URL, tsStart, tsEnd)
	jobMap[jobID] = tsStart
	ctx := context.WithValue(req.Context(), tsStartKey, jobMap)
	*req = *req.WithContext(ctx)
	log.Printf("DEBUG: Found ID in query: %s", jobID)
}

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

func overwriteTimeframe(targetURL *url.URL, tsStart, tsEnd int64) {
	q := targetURL.Query()
	q.Set("start", fmt.Sprintf("%d", tsStart))
	q.Set("end", fmt.Sprintf("%d", tsEnd))
	targetURL.RawQuery = q.Encode()
}

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

func applyTimeframeOffset(promData *PromResponse, tsStart int64) {
	for i := range promData.Data.Result {
		for j := range promData.Data.Result[i].Values {
			if len(promData.Data.Result[i].Values[j]) == 0 {
				continue
			}

			rawTs := promData.Data.Result[i].Values[j][0]
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
			promData.Data.Result[i].Values[j][0] = (ts - tsStart) 
		}
	}
}

func (t *splitQueryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctxJobIDs := req.Context().Value(jobIDsKey)
	if ctxJobIDs == nil {
		return t.base.RoundTrip(req)
	}

	jobIDs, ok := ctxJobIDs.([]string)
	if !ok || len(jobIDs) <= 1 {
		return t.base.RoundTrip(req)
	}

	originalQuery := req.URL.Query().Get("query")
	if originalQuery == "" {
		return t.base.RoundTrip(req)
	}

	log.Printf("Splitting multi-job query into %d upstream requests", len(jobIDs))

	var merged PromResponse
	merged.Status = "success"
	merged.Data.ResultType = "matrix"

	var firstResp *http.Response
	successCount := 0

	for _, jobID := range jobIDs {
		tsStart, tsEnd, err := t.ps.fetchFromCouchbase(jobID)
		if err != nil {
			log.Printf("Error fetching from Couchbase for %s: %v", jobID, err)
			continue
		}

		singleReq := req.Clone(req.Context())
		singleURL := *req.URL
		q := singleURL.Query()
		q.Set("query", replaceJobFilter(originalQuery, jobID))
		singleURL.RawQuery = q.Encode()
		overwriteTimeframe(&singleURL, tsStart, tsEnd)
		singleReq.URL = &singleURL
		singleReq.RequestURI = ""

		resp, err := t.base.RoundTrip(singleReq)
		if err != nil {
			log.Printf("Error executing upstream request for %s: %v", jobID, err)
			continue
		}

		bodyBytes, err := decodeHTTPBody(resp)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading upstream response for %s: %v", jobID, err)
			continue
		}

		var promData PromResponse
		if err := json.Unmarshal(bodyBytes, &promData); err != nil {
			log.Printf("Failed to unmarshal upstream response for %s: %v", jobID, err)
			continue
		}

		applyTimeframeOffset(&promData, tsStart)
		merged.Data.Result = append(merged.Data.Result, promData.Data.Result...)

		if firstResp == nil {
			firstResp = resp
			if promData.Data.ResultType != "" {
				merged.Data.ResultType = promData.Data.ResultType
			}
		}

		successCount++
	}

	if successCount == 0 {
		return nil, errors.New("all split upstream requests failed")
	}

	bodyBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged response: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))

	statusCode := http.StatusOK
	status := http.StatusText(http.StatusOK)
	if firstResp != nil {
		statusCode = firstResp.StatusCode
		status = firstResp.Status
	}

	return &http.Response{
		Status:        status,
		StatusCode:    statusCode,
		Header:        headers,
		Body:          io.NopCloser(bytes.NewReader(bodyBytes)),
		ContentLength: int64(len(bodyBytes)),
		Request:       req,
	}, nil
}

// logResponseBody is the function you can comment in/out or toggle via ps.Verbose
func (ps *ProxyServer) logResponseBody(contentType string, body []byte) {
	if !ps.Verbose {
		return
	}
	if contentType == "application/json" {
		log.Printf("JSON Response: %s", string(body))
	}
}

func (ps *ProxyServer) modifyResponse(resp *http.Response) error {
	var bodyBytes []byte
	var err error

	if resp.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer reader.Close()
		bodyBytes, err = io.ReadAll(reader)
		resp.Header.Del("Content-Encoding")
	} else {
		bodyBytes, err = io.ReadAll(resp.Body)
	}

	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	resp.Body.Close()

	if tsVal := resp.Request.Context().Value(tsStartKey); tsVal != nil {
		jobMap := tsVal.(map[string]int64)
		var promData PromResponse
		if err := json.Unmarshal(bodyBytes, &promData); err != nil {
			log.Printf("Failed to unmarshal Prometheus response: %v", err)
			return nil
		}

		var filtered []Result
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
				if len(promData.Data.Result[i].Values[j]) > 0 {
					rawTs := promData.Data.Result[i].Values[j][0]
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
					promData.Data.Result[i].Values[j][0] = (ts - tsStart)
				}
			}
			filtered = append(filtered, promData.Data.Result[i])
		}
		promData.Data.Result = filtered

		if modifiedBody, err := json.Marshal(promData); err == nil {
			bodyBytes = modifiedBody
		} else {
			log.Printf("Failed to marshal modified Prometheus response: %v", err)
		}
	}

	ps.logResponseBody(resp.Header.Get("Content-Type"), bodyBytes)
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))

	return nil
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	Cb, err = ConnectCouchbase(
		config.CouchbaseConnectionString,
		config.Username,
		config.Password,
		config.MetadataBucketName,
	)
	if err != nil {
		log.Fatalf("Failed to connect to Couchbase: %v", err)
	}
	server, err := NewProxyServer(config.PrometheusConnectionString)
	if err != nil {
		log.Fatalf("Init error: %v", err)
	}

	// toggle it here programmatically
	server.Verbose = true

	mux := http.NewServeMux()
	mux.HandleFunc(config.Prefix, func(w http.ResponseWriter, r *http.Request) {
		// if !strings.Contains(r.URL.Path, "/api/v1/query_range") {
		// 	// log.Printf("Passing through non-query_range request without rewriting: %s", r.URL.String())
		// 	// http.StripPrefix(config.Prefix, server.proxy).ServeHTTP(w, r)
		// 	return
		// }
		http.StripPrefix(config.Prefix, server.proxy).ServeHTTP(w, r)
	})

	log.Printf("Proxy listening on %s -> Forwarding to %s", config.ListenAddr, config.PrometheusConnectionString)
	log.Fatal(http.ListenAndServe(config.ListenAddr, mux))
}
