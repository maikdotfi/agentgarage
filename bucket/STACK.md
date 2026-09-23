# bucket stack

- [aws-sdk-go-v2/service/s3](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3) -
  R2 speaks S3, and SigV4 plus conditional-write error mapping is not worth
  hand-rolling. Only `r2.go` imports it.
- [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) - the
  token bucket behind each caller's named limit.
