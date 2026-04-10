package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
)

type contextKey string

// prometheus response types
type PromResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string   `json:"resultType"`
		Result     []Result `json:"result"`
	} `json:"data"`
}

type Result struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

// server specific types
type ProxyServer struct {
	target  *url.URL
	proxy   *httputil.ReverseProxy
	Debug   bool
	Verbose bool
}

type splitQueryTransport struct {
	base http.RoundTripper
	ps   *ProxyServer
}

// config related types
type AppConfig struct {
	ListenAddr                 string
	Prefix                     string
	CouchbaseConnectionString  string
	Username                   string
	Password                   string
	MetadataBucketName         string
	PrometheusConnectionString string
	DebugLogging               bool
}

// couchbase client types
type CouchbaseClient struct {
	Cluster *gocb.Cluster
	Bucket  *gocb.Bucket
	Debug   bool
	Cache   *expirable.LRU[string, TimeframeEntry]
	Group   singleflight.Group
}

type Metadata struct {
	Id       string `json:"id"`
	Ts_start string `json:"ts_start"`
	Ts_end   string `json:"ts_end"`
}

// metadata cache types
type TimeframeEntry struct {
	Start, End int64
	FetchedAt  time.Time
}
