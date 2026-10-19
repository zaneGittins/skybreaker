package broker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/notify"
	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
)

type countNotifier struct {
	mu  sync.Mutex
	ids []string
}

func (c *countNotifier) Notify(_ context.Context, m notify.Message) error {
	c.mu.Lock()
	c.ids = append(c.ids, m.ID)
	c.mu.Unlock()
	return nil
}

func TestApprovalPushesAreBucketed(t *testing.T) {
	// A flood of distinct asks from one project buzzes the phone once a minute.
	store := approval.New(approval.Options{MaxPending: 50})
	sent := &countNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go notifyApprovals(ctx, store, notify.Dedup(sent, 10*time.Minute), "https://skybreaker")
	time.Sleep(20 * time.Millisecond) // let it subscribe
	for i := range 10 {
		store.Request(ctx, approval.Request{Project: "orbit", Method: "POST", Host: "h", Path: "/", Body: []byte{byte(i)}}, 0)
	}
	store.Request(ctx, approval.Request{Project: "bifract", Method: "POST", Host: "h", Path: "/"}, 0)
	time.Sleep(100 * time.Millisecond)
	sent.mu.Lock()
	defer sent.mu.Unlock()
	if len(sent.ids) != 2 {
		t.Fatalf("sent %d pushes (%v), want one per project", len(sent.ids), sent.ids)
	}
}
