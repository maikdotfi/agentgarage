package bucket

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Config is where the bucket is and the token that may use it.
type R2Config struct {
	Endpoint        string // https://<account>.r2.cloudflarestorage.com
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

type r2 struct {
	s3     *s3.Client
	bucket string
}

// R2 is the Store for a Cloudflare R2 bucket, over its S3 API.
func R2(cfg R2Config) Store {
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// R2 has no use for the SDK's default checksums; send them only when required.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		// A caller that runs out of budget waits; the SDK must not retry harder.
		RetryMaxAttempts: 1,
	})
	return &r2{s3: client, bucket: cfg.Bucket}
}

func (r *r2) Get(ctx context.Context, key string) (Object, error) {
	out, err := r.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &r.bucket, Key: &key})
	if err != nil {
		return Object{}, mapErr(err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return Object{}, err
	}
	return Object{Body: body, Meta: out.Metadata, ETag: aws.ToString(out.ETag)}, nil
}

func (r *r2) Put(ctx context.Context, key string, obj Object, cond Cond) (string, error) {
	in := &s3.PutObjectInput{
		Bucket:        &r.bucket,
		Key:           &key,
		Body:          bytes.NewReader(obj.Body),
		ContentLength: aws.Int64(int64(len(obj.Body))),
		Metadata:      obj.Meta,
	}
	if cond.IfNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	if cond.IfMatch != "" {
		in.IfMatch = aws.String(cond.IfMatch)
	}
	out, err := r.s3.PutObject(ctx, in)
	if err != nil {
		return "", mapErr(err)
	}
	return aws.ToString(out.ETag), nil
}

func mapErr(err error) error {
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) {
		switch resp.HTTPStatusCode() {
		case http.StatusNotFound:
			return ErrNotFound
		case http.StatusPreconditionFailed, http.StatusConflict:
			return ErrPrecondition
		}
	}
	return err
}
