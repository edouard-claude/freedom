package pool

import "sync"

const defaultBufSize = 32 * 1024 // 32KB

// BufferPool provides reusable byte slices via sync.Pool.
type BufferPool struct {
	pool sync.Pool
}

// New creates a BufferPool with the given initial buffer size.
func New(size int) *BufferPool {
	if size <= 0 {
		size = defaultBufSize
	}
	return &BufferPool{
		pool: sync.Pool{
			New: func() any {
				b := make([]byte, size)
				return b
			},
		},
	}
}

// Get returns a byte slice from the pool.
func (p *BufferPool) Get() []byte {
	return p.pool.Get().([]byte)
}

// Put returns a byte slice to the pool.
func (p *BufferPool) Put(b []byte) {
	if cap(b) > 0 {
		p.pool.Put(b[:cap(b)])
	}
}
