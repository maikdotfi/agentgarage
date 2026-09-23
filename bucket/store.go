// Package bucket is the only way the garage talks to R2. A Client signs
// everything it writes, drops what it can't verify, counts every operation by
// billing class and makes each named caller wait its turn.
package bucket

import (
	"context"
	"errors"
)

var (
	ErrNotFound     = errors.New("bucket: not found")
	ErrExists       = errors.New("bucket: already exists")
	ErrConflict     = errors.New("bucket: changed since it was read")
	ErrBadSignature = errors.New("bucket: missing or untrusted signature")

	// ErrPrecondition is what a Store returns when a conditional write is refused.
	ErrPrecondition = errors.New("bucket: precondition failed")
)

// Object is one stored object. Meta is its user metadata, which is where the
// signature travels.
type Object struct {
	Body []byte
	Meta map[string]string
	ETag string
}

// Cond makes a Put conditional. IfNoneMatch creates only; IfMatch replaces
// only the version with that ETag.
type Cond struct {
	IfNoneMatch bool
	IfMatch     string
}

// Store is the raw object store under a Client: R2 in production, a Fake in
// tests. Every method call is one billed operation.
type Store interface {
	Get(ctx context.Context, key string) (Object, error)
	Put(ctx context.Context, key string, obj Object, cond Cond) (etag string, err error)
}

// Counts is operations by R2 billing class: A is writes and LIST, B is reads.
type Counts struct {
	ClassA int64
	ClassB int64
}
