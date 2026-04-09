package main

import (
	"log"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"
)

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
	}, nil
}

func (cb *CouchbaseClient) GetTimeframe(id string) (int64, int64, error) {
	if entry, found := cb.Cache.Load(id); found {
		if timeframe, ok := entry.(TimeframeEntry); ok && time.Since(timeframe.FetchedAt) < 24*time.Hour {
			log.Printf("Cache hit for job %s: start=%d, end=%d", id, timeframe.Start, timeframe.End)
			return timeframe.Start, timeframe.End, nil
		}
	}
	collection := cb.Bucket.DefaultCollection()

	getResult, err := collection.Get(id, nil)
	if err != nil {
		return 0, 0, err
	}

	var metadata Metadata
	err = getResult.Content(&metadata)
	if err != nil {
		return 0, 0, err
	}

	rfc3339t := strings.Replace(metadata.Ts_start, " ", "T", 1)
	tsStart, err := time.Parse(time.RFC3339, rfc3339t)
	if err != nil {
		return 0, 0, err
	}
	rfc3339t = strings.Replace(metadata.Ts_end, " ", "T", 1)
	tsEnd, err := time.Parse(time.RFC3339, rfc3339t)
	if err != nil {
		return 0, 0, err
	}

	start := tsStart.Unix()
	end := tsEnd.Unix()
	cb.Cache.Store(id, TimeframeEntry{
		Start:     start,
		End:       end,
		FetchedAt: time.Now(),
	})
	// log.Printf("Fetched start time for job %s: %s (Unix s: %d)", id, metadata.Ts_start, start)
	// log.Printf("Fetched end time for job %s: %s (Unix s: %d)", id, metadata.Ts_end, end)

	return start, end, nil
}
