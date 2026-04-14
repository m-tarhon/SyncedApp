package main

import (
	"log"
	"net/http"
	"regexp"
)

const (
	tsStartKey     contextKey = "ts_start"
	jobIDsKey      contextKey = "job_ids"
	passthroughKey contextKey = "passthrough"
)

var (
	jobIDRegex = regexp.MustCompile(`job=~?"([0-9a-fA-F\-]{36}(?:\|[0-9a-fA-F\-]{36}){0,5})"`)
	Cb         *CouchbaseClient
)

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
	server, err := NewProxyServer(config)
	if err != nil {
		log.Fatalf("Init error: %v", err)
	}

	logger := NewLogger(config.LogCache, config.LogTimeseries, config.LogSplitting)
	server.Logger = logger

	if config.MemcachedEnabled {
		cache, err := NewQueryResultCache(
			config.MemcachedAddrs,
			config.MemcachedTTLSeconds,
			config.MemcachedTimeoutMS,
			config.MemcachedMaxItemBytes,
			logger,
		)
		if err != nil {
			log.Fatalf("Failed to initialize memcached cache: %v", err)
		}
		server.Cache = cache
		log.Printf("Memcached enabled with servers: %s", config.MemcachedAddrs)
	}

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
