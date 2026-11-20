package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ObjectStore abstracts symbol bundle and remote diagnostic log blob storage (S3, MinIO, R2, GCS S3-compat).
type ObjectStore interface {
	PutObject(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) ([]byte, bool, error)
	DeleteObject(ctx context.Context, key string) error
}

// S3Config configures a zero-dependency AWS Signature V4 path-style S3/MinIO object store.
type S3Config struct {
	Endpoint  string // e.g. http://minio:9000 or https://s3.us-east-1.amazonaws.com
	Region    string // default: us-east-1
	Bucket    string
	AccessKey string
	SecretKey string
	Timeout   time.Duration
}

// S3Store implements ObjectStore using pure Go stdlib HTTP + AWS Signature Version 4.
type S3Store struct {
	endpoint  string
	host      string
	region    string
	bucket    string
	accessKey string
	secretKey string
	client    *http.Client
	nowFunc   func() time.Time
}

var _ ObjectStore = (*S3Store)(nil)

func NewS3(cfg S3Config) (*S3Store, error) {
	ep := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if ep == "" || strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("s3 endpoint and bucket are required")
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("invalid s3 endpoint: %w", err)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &S3Store{
		endpoint:  ep,
		host:      u.Host,
		region:    region,
		bucket:    strings.Trim(cfg.Bucket, "/"),
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		client:    &http.Client{Timeout: timeout},
		nowFunc:   func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *S3Store) PutObject(ctx context.Context, key string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req, err := s.signedRequest(ctx, http.MethodPut, key, data, contentType)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 PUT %s HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *S3Store) GetObject(ctx context.Context, key string) ([]byte, bool, error) {
	req, err := s.signedRequest(ctx, http.MethodGet, key, nil, "")
	if err != nil {
		return nil, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, fmt.Errorf("s3 GET %s HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(resp.Body)
	return data, true, err
}

func (s *S3Store) DeleteObject(ctx context.Context, key string) error {
	req, err := s.signedRequest(ctx, http.MethodDelete, key, nil, "")
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 DELETE %s HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *S3Store) signedRequest(ctx context.Context, method, key string, payload []byte, contentType string) (*http.Request, error) {
	cleanKey := strings.TrimLeft(key, "/")
	canonicalURI := "/" + s.bucket + "/" + cleanKey
	fullURL := s.endpoint + canonicalURI

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	now := s.nowFunc()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(payload)

	req.Header.Set("Host", s.host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	if s.accessKey != "" && s.secretKey != "" {
		canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", s.host, payloadHash, amzDate)
		signedHeaders := "host;x-amz-content-sha256;x-amz-date"
		canonicalRequest := strings.Join([]string{
			method,
			canonicalURI,
			"",
			canonicalHeaders,
			signedHeaders,
			payloadHash,
		}, "\n")

		scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, s.region)
		stringToSign := strings.Join([]string{
			"AWS4-HMAC-SHA256",
			amzDate,
			scope,
			sha256Hex([]byte(canonicalRequest)),
		}, "\n")

		kDate := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(dateStamp))
		kRegion := hmacSHA256(kDate, []byte(s.region))
		kService := hmacSHA256(kRegion, []byte("s3"))
		kSigning := hmacSHA256(kService, []byte("aws4_request"))
		sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

		authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
			s.accessKey, scope, signedHeaders, sig)
		req.Header.Set("Authorization", authHeader)
	}
	return req, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
