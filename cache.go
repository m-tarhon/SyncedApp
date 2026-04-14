package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

const cacheKeyVersion = "v1"

type cacheEnvelope struct {
	Version string `json:"version"`
	Gzip    bool   `json:"gzip"`
	Data    []byte `json:"data"`
}

type QueryResultCache struct {
	client       *memcache.Client
	ttlSeconds   int
	maxItemBytes int
	logger       *Logger
}

func NewQueryResultCache(addrs string, ttlSeconds, timeoutMS, maxItemBytes int, logger *Logger) (*QueryResultCache, error) {
	parts := strings.Split(addrs, ",")
	servers := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		servers = append(servers, trimmed)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("memcached addrs is empty")
	}

	client := memcache.New(servers...)
	client.Timeout = time.Duration(timeoutMS) * time.Millisecond

	return &QueryResultCache{
		client:       client,
		ttlSeconds:   ttlSeconds,
		maxItemBytes: maxItemBytes,
		logger:       logger,
	}, nil
}

func BuildSplitCacheKey(path string, query url.Values, passthrough bool, jobID string) string {
	q := url.Values{}
	for k, vals := range query {
		if strings.EqualFold(k, "query") {
			continue
		}
		for _, v := range vals {
			q.Add(k, v)
		}
	}

	canonical := strings.Join([]string{
		"path=" + path,
		"job=" + jobID,
		"passthrough=" + fmt.Sprintf("%t", passthrough),
		"query=" + normalizePromQL(query.Get("query")),
		"params=" + q.Encode(),
	}, "|")

	hash := sha256.Sum256([]byte(canonical))
	return "prom:" + cacheKeyVersion + ":" + hex.EncodeToString(hash[:])
}

func normalizePromQL(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func (c *QueryResultCache) Get(key, label string) (CachedSplitResult, bool, error) {
	if c == nil {
		return CachedSplitResult{}, false, nil
	}

	item, err := c.client.Get(key)
	if err != nil {
		if err == memcache.ErrCacheMiss {
			c.logger.LogCache("MISS  job=%s key=%.16s...", label, key)
			return CachedSplitResult{}, false, nil
		}
		log.Printf("[cache] ERROR get job=%s key=%.16s... err=%v", label, key, err)
		return CachedSplitResult{}, false, err
	}

	decoded, err := decodeCachedValue(item.Value)
	if err != nil {
		log.Printf("[cache] ERROR decode job=%s key=%.16s... err=%v", label, key, err)
		return CachedSplitResult{}, false, err
	}

	c.logger.LogCache("HIT   job=%s key=%.16s... series=%d", label, key, len(decoded.Result))

	return decoded, true, nil
}

func (c *QueryResultCache) Set(key, label string, value CachedSplitResult) error {
	if c == nil {
		return nil
	}

	encoded, err := encodeCachedValue(value)
	if err != nil {
		log.Printf("[cache] ERROR encode job=%s key=%.16s... err=%v", label, key, err)
		return err
	}

	if c.maxItemBytes > 0 && len(encoded) > c.maxItemBytes {
		log.Printf("[cache] SKIP  job=%s key=%.16s... bytes=%d exceeds max=%d", label, key, len(encoded), c.maxItemBytes)
		return nil
	}

	item := &memcache.Item{
		Key:        key,
		Value:      encoded,
		Expiration: int32(c.ttlSeconds),
	}

	if err := c.client.Set(item); err != nil {
		log.Printf("[cache] ERROR set  job=%s key=%.16s... err=%v", label, key, err)
		return err
	}

	c.logger.LogCache("SET   job=%s key=%.16s... bytes=%d ttl=%ds series=%d", label, key, len(encoded), c.ttlSeconds, len(value.Result))

	return nil
}

func encodeCachedValue(value CachedSplitResult) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	compressed, err := gzipBytes(raw)
	if err != nil {
		return nil, err
	}

	env := cacheEnvelope{
		Version: cacheKeyVersion,
		Gzip:    true,
		Data:    compressed,
	}

	return json.Marshal(env)
}

func decodeCachedValue(encoded []byte) (CachedSplitResult, error) {
	var env cacheEnvelope
	if err := json.Unmarshal(encoded, &env); err != nil {
		return CachedSplitResult{}, err
	}

	if env.Version != cacheKeyVersion {
		return CachedSplitResult{}, fmt.Errorf("unexpected cache version: %s", env.Version)
	}

	body := env.Data
	if env.Gzip {
		var err error
		body, err = gunzipBytes(env.Data)
		if err != nil {
			return CachedSplitResult{}, err
		}
	}

	var out CachedSplitResult
	if err := json.Unmarshal(body, &out); err != nil {
		return CachedSplitResult{}, err
	}

	return out, nil
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func gunzipBytes(raw []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	return io.ReadAll(zr)
}