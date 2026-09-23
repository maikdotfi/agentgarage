package bucket

import (
	"context"
	"maps"
	"strconv"
	"sync"
)

// Fake is an in-memory Store for tests. It honours conditional writes like R2
// does and counts every operation, refused ones included.
type Fake struct {
	mu      sync.Mutex
	objects map[string]Object
	version int
	counts  Counts
}

func NewFake() *Fake {
	return &Fake{objects: map[string]Object{}}
}

func (f *Fake) Get(_ context.Context, key string) (Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts.ClassB++
	obj, ok := f.objects[key]
	if !ok {
		return Object{}, ErrNotFound
	}
	return copyObject(obj), nil
}

func (f *Fake) Put(_ context.Context, key string, obj Object, cond Cond) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts.ClassA++
	old, exists := f.objects[key]
	if cond.IfNoneMatch && exists {
		return "", ErrPrecondition
	}
	if cond.IfMatch != "" && (!exists || old.ETag != cond.IfMatch) {
		return "", ErrPrecondition
	}
	f.version++
	obj = copyObject(obj)
	obj.ETag = `"` + strconv.Itoa(f.version) + `"`
	f.objects[key] = obj
	return obj.ETag, nil
}

// Counts is every operation the fake has seen.
func (f *Fake) Counts() Counts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts
}

func copyObject(o Object) Object {
	o.Body = append([]byte(nil), o.Body...)
	o.Meta = maps.Clone(o.Meta)
	return o
}
