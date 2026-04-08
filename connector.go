package main

import (
	"log"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"
)

type CouchbaseClient struct {
	Cluster *gocb.Cluster
	Bucket  *gocb.Bucket
}

type Metadata struct {
	Id       string `json:"id"`
	Ts_start string `json:"ts_start"`
	Ts_end   string `json:"ts_end"`
}

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
	log.Printf("Fetched start time for job %s: %s (Unix s: %d)", id, metadata.Ts_start, start)
	log.Printf("Fetched end time for job %s: %s (Unix s: %d)", id, metadata.Ts_end, end)

	return start, end, nil
}
