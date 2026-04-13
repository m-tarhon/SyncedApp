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
	"strings"
)

func (ps *ProxyServer) fetchFromCouchbase(jobID string) (int64, int64, error) {
	return Cb.GetTimeframe(jobID)
}

func (ps *ProxyServer) inspectRequest(req *http.Request) {
	ps.debugf("inspectRequest: %s %s", req.Method, req.URL.String())
	if req.Method != http.MethodGet {
		return
	}

	// Instant queries with a "time" param are converted to range queries using
	// the full Couchbase-stored timeframe; results are forwarded as-is (no offset shift).
	isInstantQuery := strings.HasSuffix(req.URL.Path, "/api/v1/query") &&
		req.URL.Query().Get("time") != ""
	if isInstantQuery {
		req.URL.Path = strings.TrimSuffix(req.URL.Path, "/query") + "/query_range"
		q := req.URL.Query()
		q.Del("time")
		req.URL.RawQuery = q.Encode()
	}

	query := req.URL.Query().Get("query")
	jobIDs := extractJobIDs(query)
	if len(jobIDs) == 0 {
		return
	}

	if len(jobIDs) > 1 {
		ctx := context.WithValue(req.Context(), jobIDsKey, jobIDs)
		if isInstantQuery {
			ctx = context.WithValue(ctx, passthroughKey, true)
		}
		*req = *req.WithContext(ctx)
		return
	}

	jobID := jobIDs[0]
	tsStart, tsEnd, err := ps.fetchFromCouchbase(jobID)
	if err != nil {
		log.Printf("Error fetching from Couchbase for %s: %v", jobID, err)
		return
	}
	overwriteTimeframe(req.URL, tsStart, tsEnd)

	if isInstantQuery {
		// Ensure query_range has a step; use 60s if none provided.
		q := req.URL.Query()
		if q.Get("step") == "" {
			q.Set("step", "60")
			req.URL.RawQuery = q.Encode()
		}
		ctx := context.WithValue(req.Context(), passthroughKey, true)
		*req = *req.WithContext(ctx)
		ps.debugf("passthrough range query for job: %s", jobID)
		return
	}

	jobMap := map[string]int64{jobID: tsStart}
	ctx := context.WithValue(req.Context(), tsStartKey, jobMap)
	*req = *req.WithContext(ctx)
	ps.debugf("found job ID in query: %s", jobID)
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

func (ps *ProxyServer) handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}

	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		ps.debugf("proxy request canceled by client: %v", err)
		return
	}

	log.Printf("proxy upstream error: %v", err)
	if r.Context().Err() != nil {
		return
	}

	http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
}
