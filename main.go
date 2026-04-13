package main

import (
	"bytes"
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
	tsStartKey     contextKey = "ts_start"
	jobIDsKey      contextKey = "job_ids"
	passthroughKey contextKey = "passthrough"
)

var (
	// Matches job="uuid" or job=~"uuid1|uuid2|..." (up to 6 UUIDs)
	jobIDRegex = regexp.MustCompile(`job=~?"([0-9a-fA-F\-]{36}(?:\|[0-9a-fA-F\-]{36}){0,5})"`)
	Cb         *CouchbaseClient
)

func NewProxyServer(config AppConfig) (*ProxyServer, error) {
	targetURL := config.PrometheusConnectionString
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	ps := &ProxyServer{
		target:  target,
		Debug:   false,
		Verbose: false,
	}

	ps.proxy = httputil.NewSingleHostReverseProxy(target)

	originalDirector := ps.proxy.Director
	ps.proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		ps.inspectRequest(req)
	}

	ps.proxy.Transport = &splitQueryTransport{
		base: newUpstreamTransport(),
		ps:   ps,
	}

	ps.proxy.ErrorHandler = ps.handleProxyError
	ps.proxy.ModifyResponse = ps.modifyResponse

	return ps, nil
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

	passthrough := req.Context().Value(passthroughKey) != nil
	t.ps.debugf("Splitting multi-job query into %d upstream requests (passthrough=%v)", len(jobIDs), passthrough)

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
			if passthrough && q.Get("step") == "" {
				q2 := singleURL.Query()
				q2.Set("step", "15")
				singleURL.RawQuery = q2.Encode()
			}
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

			if !passthrough {
				applyJobOffsetsAndFilter(&promData, map[string]int64{jobID: tsStart})
			}

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
		config.DebugLogging,
	)
	if err != nil {
		log.Fatalf("Failed to connect to Couchbase: %v", err)
	}
	server, err := NewProxyServer(config)
	if err != nil {
		log.Fatalf("Init error: %v", err)
	}

	server.Debug = config.DebugLogging
	server.Verbose = config.DebugLogging

	mux := http.NewServeMux()
	mux.HandleFunc(config.Prefix, func(w http.ResponseWriter, r *http.Request) {
		// if !strings.Contains(r.URL.Path, "/api/v1/query_range") {
		// 	// log.Printf("Passing through non-query_range request without rewriting: %s", r.URL.String())
		// 	// http.StripPrefix(config.Prefix, server.proxy).ServeHTTP(w, r)
		// 	return
		// }
		http.StripPrefix(config.Prefix, server.proxy).ServeHTTP(w, r)
	})

	handler := limitMiddleware(maxInFlightRequests, mux)
	httpServer := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}

	log.Printf("Proxy listening on %s -> Forwarding to %s", config.ListenAddr, config.PrometheusConnectionString)
	log.Fatal(httpServer.ListenAndServe())
}
