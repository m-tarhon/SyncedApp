package main

import (
	"net/http"
	"time"
)

const (
	maxInFlightRequests     = 512
	maxQueueWait            = 150 * time.Second
	serverReadHeaderTimeout = 5 * time.Second
	serverWriteTimeout      = 120 * time.Second
	serverIdleTimeout       = 120 * time.Second
	serverMaxHeaderBytes    = 1 << 20
	upstreamMaxIdleConns    = 256
	upstreamMaxConnsPerHost = 256
	upstreamIdleConnTimeout = 90 * time.Second
	upstreamResponseTimeout = 30 * time.Second
)

func newUpstreamTransport(config AppConfig) *http.Transport {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          upstreamMaxIdleConns,
			MaxIdleConnsPerHost:   upstreamMaxConnsPerHost,
			MaxConnsPerHost:       upstreamMaxConnsPerHost,
			IdleConnTimeout:       upstreamIdleConnTimeout,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: upstreamResponseTimeout,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}

	transport := baseTransport.Clone()
	transport.MaxIdleConns = upstreamMaxIdleConns
	transport.MaxIdleConnsPerHost = upstreamMaxConnsPerHost
	transport.MaxConnsPerHost = upstreamMaxConnsPerHost
	transport.IdleConnTimeout = upstreamIdleConnTimeout
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = upstreamResponseTimeout
	transport.ExpectContinueTimeout = 1 * time.Second

	return transport
}

func limitMiddleware(maxInFlight int, next http.Handler) http.Handler {
	if maxInFlight <= 0 {
		return next
	}

	sem := make(chan struct{}, maxInFlight)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(maxQueueWait)
		defer timer.Stop()

		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			return
		case <-timer.C:
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		default:
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				next.ServeHTTP(w, r)
			case <-r.Context().Done():
				return
			case <-timer.C:
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			}
		}
	})
}
