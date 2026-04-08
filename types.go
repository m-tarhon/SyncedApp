package main

import (
	"net/http/httputil"
	"net/url"
)

type contextKey string 

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

type ProxyServer struct {
	target  *url.URL
	proxy   *httputil.ReverseProxy
	Verbose bool
}

