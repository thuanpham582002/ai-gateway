// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3BodyStoreConfig configures an S3-compatible body store such as SeaweedFS or MinIO.
type S3BodyStoreConfig struct {
	Endpoint             string
	Bucket               string
	Region               string
	Prefix               string
	CABundlePath         string
	CAPEM                string
	UsePathStyle         bool
	MaxBodyBytes         int64
	UploadTimeout        time.Duration
	ServerSideEncryption string
	KMSKeyID             string
}

type s3PutObjectAPI interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type s3BodyStore struct {
	client               s3PutObjectAPI
	bucket               string
	prefix               string
	maxBodyBytes         int64
	uploadTimeout        time.Duration
	serverSideEncryption types.ServerSideEncryption
	kmsKeyID             string
}

// NewS3BodyStore creates a store using the standard AWS credential chain.
func NewS3BodyStore(ctx context.Context, cfg S3BodyStoreConfig) (BodyStore, func(), error) {
	if cfg.Bucket == "" {
		return nil, nil, fmt.Errorf("S3 body bucket is required")
	}
	if cfg.MaxBodyBytes <= 0 {
		return nil, nil, fmt.Errorf("S3 maximum body bytes must be positive")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = 15 * time.Second
	}
	if cfg.Endpoint != "" {
		parsed, err := url.Parse(cfg.Endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return nil, nil, fmt.Errorf("S3 endpoint must be an HTTP(S) URL without user information")
		}
	}

	transport, err := s3Transport(cfg.CABundlePath, cfg.CAPEM)
	if err != nil {
		return nil, nil, err
	}
	httpClient := &http.Client{Transport: transport}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	// Older S3-compatible implementations do not consistently support the SDK's
	// optional trailing checksum protocol. The event and object metadata retain SHA-256.
	awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(strings.TrimRight(cfg.Endpoint, "/"))
		}
	})
	store := &s3BodyStore{
		client:        client,
		bucket:        cfg.Bucket,
		prefix:        strings.Trim(cfg.Prefix, "/"),
		maxBodyBytes:  cfg.MaxBodyBytes,
		uploadTimeout: cfg.UploadTimeout,
		kmsKeyID:      cfg.KMSKeyID,
	}
	switch cfg.ServerSideEncryption {
	case "":
	case string(types.ServerSideEncryptionAes256):
		store.serverSideEncryption = types.ServerSideEncryptionAes256
	case string(types.ServerSideEncryptionAwsKms):
		store.serverSideEncryption = types.ServerSideEncryptionAwsKms
	default:
		return nil, nil, fmt.Errorf("S3 server-side encryption must be empty, AES256, or aws:kms")
	}
	if cfg.KMSKeyID != "" && store.serverSideEncryption != types.ServerSideEncryptionAwsKms {
		return nil, nil, fmt.Errorf("S3 KMS key ID requires aws:kms server-side encryption")
	}

	return store, transport.CloseIdleConnections, nil
}

func s3Transport(caBundlePath, caPEM string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig, err := tlsConfigWithAdditionalCA(caBundlePath, caPEM)
	if err != nil {
		return nil, fmt.Errorf("configure S3 TLS: %w", err)
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

func (s *s3BodyStore) MaxBodyBytes() int64 { return s.maxBodyBytes }

func (s *s3BodyStore) Put(ctx context.Context, object BodyObject) (*BodyObjectReference, error) {
	uploadCtx, cancel := context.WithTimeout(ctx, s.uploadTimeout)
	defer cancel()

	timestamp := object.Timestamp.UTC()
	key := path.Join(s.prefix, timestamp.Format("2006/01/02"), object.EventID, object.Kind+".bin")
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(object.Body),
		ContentLength: aws.Int64(int64(len(object.Body))),
		ContentType:   aws.String("application/octet-stream"),
		Metadata: map[string]string{
			"event-id":   object.EventID,
			"request-id": object.RequestID,
			"body-kind":  object.Kind,
			"sha256":     strings.TrimPrefix(object.SHA256, "sha256:"),
		},
	}
	if s.serverSideEncryption != "" {
		input.ServerSideEncryption = s.serverSideEncryption
	}
	if s.kmsKeyID != "" {
		input.SSEKMSKeyId = aws.String(s.kmsKeyID)
	}
	output, err := s.client.PutObject(uploadCtx, input)
	if err != nil {
		return nil, fmt.Errorf("put S3 body object: %w", err)
	}
	return &BodyObjectReference{
		Provider:  "s3",
		Bucket:    s.bucket,
		Key:       key,
		ETag:      strings.Trim(aws.ToString(output.ETag), `"`),
		VersionID: aws.ToString(output.VersionId),
	}, nil
}
