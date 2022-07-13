package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/fatih/color"
)

type Config struct {
	Type  string `json:"type" validate:"omitempty,oneof=empty s3"`
	Debug bool
	S3    *S3Config `json:"s3,omitempty" validate:"omitempty,required_if=Type s3,dive"`
}

type Archive interface {
	History(context.Context) ([][]byte, error)
	Put(context.Context, interface{}) error
}

func New(ctx context.Context, cfg *Config) (Archive, error) {
	switch cfg.Type {
	case "", "empty":
		return &Empty{}, nil
	case "s3":
		return NewS3(ctx, cfg.S3, cfg.Debug)
	default:
		return nil, fmt.Errorf("unsupported type: %s", cfg.Type)
	}
}

// =============================================================================

type Empty struct{}

func (a *Empty) History(context.Context) ([][]byte, error) {
	return nil, nil
}

func (a *Empty) Put(context.Context, interface{}) error {
	return nil
}

// =============================================================================

type (
	S3Config struct {
		Bucket      string         `json:"bucket" validate:"required"`
		Key         string         `json:"key" validate:"required"`
		Region      string         `json:"region" validate:"required"`
		MaxVersions int            `json:"max_versions"`
		Credentials *S3Credentials `json:"credentials,omitempty" validate:"omitempty,dive"`
	}

	S3Credentials struct {
		AccessKey    string `json:"access_key" validate:"required_with=SecretKey"`
		SecretKey    string `json:"secret_key" validate:"required_with=AccessKey"`
		SessionToken string `json:"session_token"`
	}

	S3 struct {
		cfg    *S3Config
		client *s3.Client
		debug  bool
	}
)

func NewS3(ctx context.Context, cfg *S3Config, debug bool) (*S3, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithDefaultRegion(cfg.Region),
	}
	if creds := cfg.Credentials; creds != nil {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKey, creds.SecretKey, creds.SessionToken)))
	}

	sess, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("error loading aws config: %v", err)
	}

	return &S3{
		cfg:    cfg,
		client: s3.NewFromConfig(sess),
		debug:  debug,
	}, nil
}

func (a *S3) History(ctx context.Context) (versions [][]byte, err error) {
	var n int

	params := &s3.ListObjectVersionsInput{
		Bucket: &a.cfg.Bucket,
		Prefix: &a.cfg.Key,
	}

	for {
		// retrieve a batch of object versions
		page, err := a.client.ListObjectVersions(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("error listing object versions: %v", err)
		}

		if a.debug {
			color.Yellow("retrieving batch of archived versions: %v", page)
		}

		var lastKey, lastVersionID string
		for _, item := range page.Versions {
			lastKey, lastVersionID = *item.Key, *item.VersionId

			// ignore keys that don't match
			if *item.Key != a.cfg.Key {
				continue
			}

			body, err := a.downloadObjectVersion(ctx, &item)
			if err != nil {
				return nil, err
			}

			if a.debug {
				color.Yellow("adding archived version to history: %s", string(body))
			}

			versions, n = append(versions, body), n+1

			// return early if we've
			if max := a.cfg.MaxVersions; max > 0 && n >= max {
				if a.debug {
					color.Yellow("truncating archive history: max version limit %d reached", max)
				}
				a.reverse(versions)
				return versions, nil
			}
		}

		// return if last page
		if !page.IsTruncated || len(page.Versions) == 0 {
			if a.debug {
				color.Yellow("reached end of archive history")
			}
			a.reverse(versions)
			return versions, nil
		}

		// otherwise, update pagination parameters before next iteration
		params.KeyMarker, params.VersionIdMarker = page.NextKeyMarker, page.NextVersionIdMarker
		if *params.KeyMarker == "" {
			params.KeyMarker, params.VersionIdMarker = &lastKey, &lastVersionID
		}
	}
}

func (a *S3) Put(ctx context.Context, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("error serializing version json: %v", err)
	}

	params := &s3.PutObjectInput{
		Bucket: &a.cfg.Bucket,
		Key:    &a.cfg.Key,
		Body:   bytes.NewReader(b),
	}

	_, err = a.client.PutObject(ctx, params)
	return err
}

func (a *S3) downloadObjectVersion(ctx context.Context, v *types.ObjectVersion) ([]byte, error) {
	// download object version
	version, err := a.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:    &a.cfg.Bucket,
		Key:       v.Key,
		VersionId: v.VersionId,
	})
	if err != nil {
		return nil, fmt.Errorf("error downloading object version: %v", err)
	}
	defer version.Body.Close()

	// add object version payload bytes to return value
	body, err := ioutil.ReadAll(version.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading object version content: %v", err)
	}
	return body, nil
}

func (a *S3) reverse(versions [][]byte) {
	inputLen := len(versions)
	inputMid := inputLen / 2

	for i := 0; i < inputMid; i++ {
		j := inputLen - i - 1

		versions[i], versions[j] = versions[j], versions[i]
	}
}
