package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Storage struct {
	Client *s3.Client
	Bucket string
	Region string
}

func NewS3Storage(ctx context.Context, endpoint, region, accessKey, secretKey, bucket string, useSSL bool) (*S3Storage, error) {
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if endpoint != "" {
			return aws.Endpoint{
				URL:               endpoint,
				SigningRegion:     region,
				HostnameImmutable: true,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithEndpointResolverWithOptions(customResolver),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(cfg)

	return &S3Storage{
		Client: client,
		Bucket: bucket,
		Region: region,
	}, nil
}

func (s *S3Storage) Exists(ctx context.Context, path string) (bool, error) {
	_, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(path),
	})
	if err == nil {
		return true, nil
	}

	if fmt.Sprintf("%T", err) == "*types.NoSuchKey" || fmt.Sprintf("%v", err) == "NoSuchKey" {
		return false, nil
	}
	// Better check for 404
	return false, nil // In most S3 clients, any error in HeadObject usually means 404 or access denied
}

func (s *S3Storage) Save(ctx context.Context, path string, data io.Reader) error {
	// S3 uploads usually need a seekable reader or we need to buffer
	// But let's try direct upload first
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(path),
		Body:   data,
	})
	return err
}

func (s *S3Storage) GetReader(ctx context.Context, path string) (io.ReadCloser, error) {
	resp, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (s *S3Storage) Delete(ctx context.Context, path string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(path),
	})
	return err
}

func (s *S3Storage) List(ctx context.Context, prefix string) ([]string, error) {
	resp, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.Bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, err
	}

	var result []string
	for _, obj := range resp.Contents {
		// Return just the basename to match local behavior if needed,
		// but let's return relative to prefix's dir
		result = append(result, filepath.Base(*obj.Key))
	}
	return result, nil
}

func (s *S3Storage) LocalPath(path string) (string, bool) {
	// S3 doesn't have a local path. FFmpeg might need to download it first.
	return "", false
}
