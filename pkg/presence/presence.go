package presence

import (
	"context"
	"sync"
	"time"

	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/health"
	"github.com/tomer/waker/pkg/store"
)

type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
	StatusWaking  Status = "waking"
	StatusUnknown Status = "unknown"
)

type HostStatusInfo struct {
	Name       string               `json:"name"`
	Status     Status               `json:"status"`
	Latency    time.Duration        `json:"latency"`
	LastSeen   time.Time            `json:"last_seen,omitempty"`
	LastWake   time.Time            `json:"last_wake,omitempty"`
	Checks     []health.CheckResult `json:"checks,omitempty"`
	PolledAt   time.Time            `json:"polled_at"`
}

type Poller struct {
	cfg   *config.Config
	store *store.Store

	mu       sync.RWMutex
	statuses map[string]*HostStatusInfo
	waking   map[string]time.Time // host name -> wake time

	listeners []chan map[string]*HostStatusInfo
}

func NewPoller(cfg *config.Config, st *store.Store) *Poller {
	p := &Poller{
		cfg:      cfg,
		store:    st,
		statuses: make(map[string]*HostStatusInfo),
		waking:   make(map[string]time.Time),
	}
	// Pre-populate with store data
	for _, h := range cfg.Hosts {
		hst := st.Get(h.Name)
		stat := StatusUnknown
		if hst.LastStatus != "" {
			stat = Status(hst.LastStatus)
		}
		p.statuses[h.Name] = &HostStatusInfo{
			Name:     h.Name,
			Status:   stat,
			Latency:  hst.LastLatency,
			LastSeen: hst.LastSeen,
			LastWake: hst.LastWake,
			PolledAt: time.Now(),
		}
	}
	return p
}

// MarkWaking marks a host as waking in memory for the next duration or until it turns online.
func (p *Poller) MarkWaking(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waking[name] = time.Now()
	if info, ok := p.statuses[name]; ok {
		info.Status = StatusWaking
	}
	p.store.RecordWake(name)
	_ = p.store.Save()
}

// PollOnce polls all hosts concurrently using a worker pool.
func (p *Poller) PollOnce(ctx context.Context) map[string]*HostStatusInfo {
	hosts := p.cfg.Hosts
	probeTimeout := p.cfg.Defaults.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 2 * time.Second
	}

	results := make(chan *HostStatusInfo, len(hosts))
	sem := make(chan struct{}, 10) // Concurrency worker pool limit 10

	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func(host config.HostConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			info := p.probeHost(ctx, &host, probeTimeout)
			results <- info
		}(h)
	}

	wg.Wait()
	close(results)

	p.mu.Lock()
	newMap := make(map[string]*HostStatusInfo)
	for info := range results {
		p.statuses[info.Name] = info
		newMap[info.Name] = info

		// Persist status
		p.store.RecordStatus(info.Name, string(info.Status))
		if info.Status == StatusOnline {
			p.store.RecordSeen(info.Name, info.Latency)
			delete(p.waking, info.Name)
		}
	}
	_ = p.store.Save()
	listeners := append([]chan map[string]*HostStatusInfo(nil), p.listeners...)
	p.mu.Unlock()

	// Notify listeners
	for _, ch := range listeners {
		select {
		case ch <- newMap:
		default:
		}
	}

	return newMap
}

// ProbeOne probes a single host immediately.
func (p *Poller) ProbeOne(ctx context.Context, host *config.HostConfig) *HostStatusInfo {
	probeTimeout := p.cfg.Defaults.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 2 * time.Second
	}
	info := p.probeHost(ctx, host, probeTimeout)

	p.mu.Lock()
	p.statuses[info.Name] = info
	p.store.RecordStatus(info.Name, string(info.Status))
	if info.Status == StatusOnline {
		p.store.RecordSeen(info.Name, info.Latency)
		delete(p.waking, info.Name)
	}
	_ = p.store.Save()
	p.mu.Unlock()

	return info
}

func (p *Poller) probeHost(ctx context.Context, host *config.HostConfig, timeout time.Duration) *HostStatusInfo {
	p.mu.RLock()
	wakeTime, isWaking := p.waking[host.Name]
	p.mu.RUnlock()

	st := p.store.Get(host.Name)
	res := health.CheckHost(ctx, host, timeout)
	reachable, latency := health.IsReachable(res)

	status := StatusOffline
	if reachable {
		status = StatusOnline
	} else if isWaking {
		// If wake was sent within host timeout (or default 120s), still waking
		wakeLimit := p.cfg.Defaults.Timeout
		if wakeLimit <= 0 {
			wakeLimit = 120 * time.Second
		}
		if time.Since(wakeTime) < wakeLimit {
			status = StatusWaking
		}
	}

	lastSeen := st.LastSeen
	if status == StatusOnline {
		lastSeen = time.Now()
	}

	return &HostStatusInfo{
		Name:     host.Name,
		Status:   status,
		Latency:  latency,
		LastSeen: lastSeen,
		LastWake: st.LastWake,
		Checks:   res,
		PolledAt: time.Now(),
	}
}

// GetAll returns a copy of the current status map.
func (p *Poller) GetAll() map[string]*HostStatusInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	res := make(map[string]*HostStatusInfo, len(p.statuses))
	for k, v := range p.statuses {
		copyInfo := *v
		res[k] = &copyInfo
	}
	return res
}

// Get returns status for a single host.
func (p *Poller) Get(name string) *HostStatusInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if info, ok := p.statuses[name]; ok {
		copyInfo := *info
		return &copyInfo
	}
	return nil
}

// Subscribe returns a channel receiving status updates on every poll.
func (p *Poller) Subscribe() chan map[string]*HostStatusInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan map[string]*HostStatusInfo, 5)
	p.listeners = append(p.listeners, ch)
	return ch
}

// Unsubscribe removes a channel listener.
func (p *Poller) Unsubscribe(ch chan map[string]*HostStatusInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, l := range p.listeners {
		if l == ch {
			p.listeners = append(p.listeners[:i], p.listeners[i+1:]...)
			break
		}
	}
}

// StartBackgroundLoop runs polling in a background goroutine until ctx is done.
func (p *Poller) StartBackgroundLoop(ctx context.Context) {
	interval := p.cfg.Defaults.PollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.PollOnce(ctx)
			}
		}
	}()
}
