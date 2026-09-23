package bucket

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// sigMeta is the metadata key the signature travels in.
const sigMeta = "garage-sig"

// Limit is one caller's token bucket: a token every Every, at most Burst saved.
type Limit struct {
	Every time.Duration
	Burst int
}

// Client is the budgeted, signing bucket client. Get it a Caller to use it.
type Client struct {
	store   Store
	key     ed25519.PrivateKey
	trusted []ed25519.PublicKey
	callers map[string]*Caller

	mu     sync.Mutex
	counts Counts
}

// New builds a client that signs with key and accepts objects signed by
// itself or by one of trusted. Every caller that will use it must have a limit.
func New(store Store, key ed25519.PrivateKey, trusted []ed25519.PublicKey, limits map[string]Limit) *Client {
	c := &Client{
		store:   store,
		key:     key,
		trusted: append([]ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, trusted...),
		callers: map[string]*Caller{},
	}
	for name, l := range limits {
		c.callers[name] = &Caller{c: c, name: name, limiter: rate.NewLimiter(rate.Every(l.Every), l.Burst)}
	}
	return c
}

// Caller is the client as seen by one named caller, under that caller's limit.
// It panics on a name with no limit: that is a wiring mistake.
func (c *Client) Caller(name string) *Caller {
	caller, ok := c.callers[name]
	if !ok {
		panic(fmt.Sprintf("bucket: no limit for caller %q", name))
	}
	return caller
}

// Counts is every operation this client has sent, by billing class.
func (c *Client) Counts() Counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts
}

func (c *Client) count(classA bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if classA {
		c.counts.ClassA++
	} else {
		c.counts.ClassB++
	}
}

// Caller does bucket operations on behalf of one named caller.
type Caller struct {
	c       *Client
	name    string
	limiter *rate.Limiter
}

// Get reads a verified object. Unsigned or untrusted objects are ErrBadSignature.
func (cl *Caller) Get(ctx context.Context, key string) (Object, error) {
	if err := cl.limiter.Wait(ctx); err != nil {
		return Object{}, fmt.Errorf("bucket %s: %w", cl.name, err)
	}
	cl.c.count(false)
	obj, err := cl.c.store.Get(ctx, key)
	if err != nil {
		return Object{}, err
	}
	if !cl.c.verify(key, obj) {
		return Object{}, fmt.Errorf("%s: %w", key, ErrBadSignature)
	}
	return obj, nil
}

// Put writes body to key, replacing whatever is there.
func (cl *Caller) Put(ctx context.Context, key string, body []byte) (etag string, err error) {
	return cl.put(ctx, key, body, Cond{})
}

// Create writes body only if key does not exist yet, else ErrExists.
func (cl *Caller) Create(ctx context.Context, key string, body []byte) (etag string, err error) {
	etag, err = cl.put(ctx, key, body, Cond{IfNoneMatch: true})
	if errors.Is(err, ErrPrecondition) {
		return "", fmt.Errorf("%s: %w", key, ErrExists)
	}
	return etag, err
}

// Swap replaces key only if it is still at etag, else ErrConflict.
func (cl *Caller) Swap(ctx context.Context, key string, body []byte, etag string) (string, error) {
	etag, err := cl.put(ctx, key, body, Cond{IfMatch: etag})
	if errors.Is(err, ErrPrecondition) {
		return "", fmt.Errorf("%s: %w", key, ErrConflict)
	}
	return etag, err
}

func (cl *Caller) put(ctx context.Context, key string, body []byte, cond Cond) (string, error) {
	if err := cl.limiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("bucket %s: %w", cl.name, err)
	}
	cl.c.count(true)
	sig := ed25519.Sign(cl.c.key, signed(key, body))
	obj := Object{Body: body, Meta: map[string]string{sigMeta: base64.StdEncoding.EncodeToString(sig)}}
	return cl.c.store.Put(ctx, key, obj, cond)
}

func (c *Client) verify(key string, obj Object) bool {
	sig, err := base64.StdEncoding.DecodeString(obj.Meta[sigMeta])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := signed(key, obj.Body)
	for _, pub := range c.trusted {
		if ed25519.Verify(pub, msg, sig) {
			return true
		}
	}
	return false
}

// signed is what a signature covers: the key as well as the body, so a signed
// object can't be replayed under another key.
func signed(key string, body []byte) []byte {
	msg := make([]byte, 0, len("garage-v1\x00")+len(key)+1+len(body))
	msg = append(msg, "garage-v1\x00"...)
	msg = append(msg, key...)
	msg = append(msg, 0)
	return append(msg, body...)
}
