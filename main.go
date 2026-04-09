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

	"golang.org/x/sync/errgroup"
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

func (ps *ProxyServer) fetchFromCouchbase(jobID string) (int64, int64, error) {
	log.Printf("implement this for %s", jobID)
	return Cb.GetTimeframe(jobID)
}

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

	type splitResult struct {
		statusCode int
		status     string
		resultType string
		result     []Result
		success    bool
	}

	results := make([]splitResult, len(jobIDs))
	g, gctx := errgroup.WithContext(req.Context())
	g.SetLimit(6)

	for idx, jobID := range jobIDs {
		idx := idx
		jobID := jobID

		g.Go(func() error {
			tsStart, tsEnd, err := t.ps.fetchFromCouchbase(jobID)
			if err != nil {
				log.Printf("Error fetching from Couchbase for %s: %v", jobID, err)
				return nil
			}

			singleReq := req.Clone(gctx)
			singleURL := *req.URL
			q := singleURL.Query()
			q.Set("query", replaceJobFilter(originalQuery, jobID))
			singleURL.RawQuery = q.Encode()
			overwriteTimeframe(&singleURL, tsStart, tsEnd)
			singleReq.URL = &singleURL
			singleReq.RequestURI = ""

			resp, err := t.base.RoundTrip(singleReq)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				log.Printf("Error executing upstream request for %s: %v", jobID, err)
				return nil
			}

			bodyBytes, err := decodeHTTPBody(resp)
			resp.Body.Close()
			if err != nil {
				log.Printf("Error reading upstream response for %s: %v", jobID, err)
				return nil
			}

			var promData PromResponse
			if err := json.Unmarshal(bodyBytes, &promData); err != nil {
				log.Printf("Failed to unmarshal upstream response for %s: %v", jobID, err)
				return nil
			}

			applyJobOffsetsAndFilter(&promData, map[string]int64{jobID: tsStart})

			results[idx] = splitResult{
				statusCode: resp.StatusCode,
				status:     resp.Status,
				resultType: promData.Data.ResultType,
				result:     promData.Data.Result,
				success:    true,
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	successCount := 0
	statusCode := http.StatusOK
	status := http.StatusText(http.StatusOK)

	for _, r := range results {
		if !r.success {
			continue
		}

		merged.Data.Result = append(merged.Data.Result, r.result...)
		if successCount == 0 {
			statusCode = r.statusCode
			status = r.status
			if r.resultType != "" {
				merged.Data.ResultType = r.resultType
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

		applyJobOffsetsAndFilter(&promData, jobMap)

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
