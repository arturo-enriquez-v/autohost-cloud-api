package platform

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Client wraps an S3 client configured for Cloudflare R2.
type R2Client struct {
	client     *s3.Client
	bucketName string
}

// NewR2Client creates an R2Client from environment variables:
//
//	R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_BUCKET_NAME
func NewR2Client() (*R2Client, error) {
	accountID := os.Getenv("R2_ACCOUNT_ID")
	accessKey := os.Getenv("R2_ACCESS_KEY_ID")
	secretKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	bucket := os.Getenv("R2_BUCKET_NAME")

	if accountID == "" || accessKey == "" || secretKey == "" || bucket == "" {
		return nil, fmt.Errorf("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY and R2_BUCKET_NAME must be set")
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "auto",
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	})

	return &R2Client{client: client, bucketName: bucket}, nil
}

// GetObject fetches the object at the given key and returns its content.
func (r *R2Client) GetObject(ctx context.Context, key string) ([]byte, error) {
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("R2 GetObject %q: %w", key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("R2 read body %q: %w", key, err)
	}
	return data, nil
}
