package client

import (
	"math/rand"
	"sync"
	"time"
)

type Backoff struct {
	Initial time.Duration
	Maximum time.Duration
	Jitter  float64

	mu   sync.Mutex
	rand *rand.Rand
}

func NewBackoff(initial, maximum time.Duration, jitter float64) *Backoff {
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	if maximum <= 0 {
		maximum = 30 * time.Second
	}
	if jitter < 0 {
		jitter = 0
	}
	return &Backoff{Initial: initial, Maximum: maximum, Jitter: jitter, rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func (b *Backoff) Duration(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := b.Initial
	for index := 1; index < attempt && delay < b.Maximum; index++ {
		if delay > b.Maximum/2 {
			delay = b.Maximum
			break
		}
		delay *= 2
	}
	if delay > b.Maximum {
		delay = b.Maximum
	}
	if b.Jitter == 0 {
		return delay
	}
	b.mu.Lock()
	factor := 1 + ((b.rand.Float64()*2 - 1) * b.Jitter)
	b.mu.Unlock()
	result := time.Duration(float64(delay) * factor)
	if result < 0 {
		return 0
	}
	if result > b.Maximum {
		return b.Maximum
	}
	return result
}
