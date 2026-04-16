package main

import (
	"errors"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	timeframeCacheSize = 10000
	timeframeCacheTTL  = 24 * time.Hour
)

var ErrUnexpectedTimeframeType = errors.New("unexpected timeframe cache value type")

func ConnectCouchbase(connectionString, username, password, bucketName string) (*CouchbaseClient, error) {
	opts := gocb.ClusterOptions{
		Authenticator: gocb.PasswordAuthenticator{
			Username: username,
			Password: password,
		},
		TimeoutsConfig: gocb.TimeoutsConfig{
			ConnectTimeout: 5 * time.Second,
		},
	}
	cluster, err := gocb.Connect(connectionString, opts)
	if err != nil {
		return nil, err
	}

	bucket := cluster.Bucket(bucketName)
	err = bucket.WaitUntilReady(5*time.Second, nil)
	if err != nil {
		return nil, err
	}

	return &CouchbaseClient{
		Cluster: cluster,
		Bucket:  bucket,
		Cache:   expirable.NewLRU[string, TimeframeEntry](timeframeCacheSize, nil, timeframeCacheTTL),
	}, nil
}

func (cb *CouchbaseClient) GetTimeframe(id string) (int64, int64, error) {
	if timeframe, found := cb.Cache.Get(id); found {
		return timeframe.Start, timeframe.End, nil
	}

	value, err, _ := cb.Group.Do(id, func() (any, error) {
		if timeframe, found := cb.Cache.Get(id); found {
			return timeframe, nil
		}

		collection := cb.Bucket.DefaultCollection()
		getResult, err := collection.Get(id, nil)
		if err != nil {
			return TimeframeEntry{}, err
		}

		var metadata Metadata
		if err := getResult.Content(&metadata); err != nil {
			return TimeframeEntry{}, err
		}

		rfc3339t := strings.Replace(metadata.Ts_start, " ", "T", 1)
		tsStart, err := time.Parse(time.RFC3339, rfc3339t)
		if err != nil {
			return TimeframeEntry{}, err
		}
		rfc3339t = strings.Replace(metadata.Ts_end, " ", "T", 1)
		tsEnd, err := time.Parse(time.RFC3339, rfc3339t)
		if err != nil {
			return TimeframeEntry{}, err
		}

		timeframe := TimeframeEntry{
			Start:     tsStart.Unix(),
			End:       tsEnd.Unix(),
			FetchedAt: time.Now(),
		}
		cb.Cache.Add(id, timeframe)

		return timeframe, nil
	})
	if err != nil {
		return 0, 0, err
	}

	timeframe, ok := value.(TimeframeEntry)
	if !ok {
		return 0, 0, ErrUnexpectedTimeframeType
	}

	return timeframe.Start, timeframe.End, nil
}
