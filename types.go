package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/couchbase/gocb/v2"
)

type contextKey string 

// prometheus response types
type PromResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
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
}

// couchbase client types 
type CouchbaseClient struct {
	Cluster *gocb.Cluster
	Bucket  *gocb.Bucket
}

type Metadata struct {
	Id       string `json:"id"`
	Ts_start string `json:"ts_start"`
	Ts_end   string `json:"ts_end"`
}
