package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

type cacheJob struct {
	dir, name string
	value     any // immutable snapshot; nil deletes the cache file
}

// One short-lived worker serializes cache writes across workspace instances.
// Pending writes to the same file are replaced by the newest snapshot. This
// also orders deletions after older writes, so a delayed save cannot resurrect
// a deleted cache entry. The queue retains nothing when idle.
var cacheJobs struct {
	sync.Mutex
	pending map[string]cacheJob
	order   []string
	done    chan struct{}
}

func queueCache(dir, name string, value any) {
	if dir == "" {
		return
	}
	key := filepath.Join(dir, name)
	cacheJobs.Lock()
	defer cacheJobs.Unlock()
	if cacheJobs.done == nil {
		cacheJobs.done = make(chan struct{})
		cacheJobs.pending = map[string]cacheJob{}
		go drainCache()
	}
	if _, exists := cacheJobs.pending[key]; !exists {
		cacheJobs.order = append(cacheJobs.order, key)
	}
	cacheJobs.pending[key] = cacheJob{dir: dir, name: name, value: value}
}

func drainCache() {
	for {
		cacheJobs.Lock()
		if len(cacheJobs.order) == 0 {
			close(cacheJobs.done)
			cacheJobs.done, cacheJobs.pending, cacheJobs.order = nil, nil, nil
			cacheJobs.Unlock()
			return
		}
		key := cacheJobs.order[0]
		cacheJobs.order[0] = ""
		cacheJobs.order = cacheJobs.order[1:]
		job := cacheJobs.pending[key]
		delete(cacheJobs.pending, key)
		cacheJobs.Unlock()
		if job.value == nil {
			_ = os.Remove(key)
		} else {
			writeCacheFile(job.dir, job.name, job.value)
		}
	}
}

// FlushCache waits for queued snapshots, for orderly shutdown or reopening a
// cache immediately. Fetching never writes the cache: only accepted UI state
// is persisted, so obsolete background fetches cannot overwrite saved edits.
func FlushCache(ctx context.Context) error {
	cacheJobs.Lock()
	done := cacheJobs.done
	cacheJobs.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) cacheCol(c *Col) {
	if s.cacheDir != "" && c.Data != nil {
		queueCache(s.cacheDir, filepath.Join("collections", safeName(c.Summary.UID)+".json"), cachedCol{Summary: c.Summary, Data: c.Data.Clone()})
	}
}

func (s *Store) cacheEnv(e *Env) {
	if s.cacheDir != "" && e.Data != nil {
		queueCache(s.cacheDir, filepath.Join("environments", safeName(e.Summary.UID)+".json"), cachedEnv{Summary: e.Summary, Data: e.Data.Clone()})
	}
}

func (s *Store) changed(key string) {
	if s.versions == nil {
		s.versions = map[string]uint64{}
	}
	s.versions[key]++
}
