package skillv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrVisualCatalogUntrusted = errors.New("skillv2: visual catalog is not trusted")
	ErrVisualLoaderRequired   = errors.New("skillv2: visual asset loader is required")
	ErrVisualAssetInUse       = errors.New("skillv2: visual asset plan is in use")
	ErrVisualDigestCollision  = errors.New("skillv2: presentation digest maps to different plan content")
)

type VisualCatalogTrust interface {
	VerifyCatalog(revision, digest string) error
}

type TrustedVisualCatalogs map[string]string

func (catalogs TrustedVisualCatalogs) VerifyCatalog(revision, digest string) error {
	if expected, ok := catalogs[revision]; !ok || expected != digest {
		return ErrVisualCatalogUntrusted
	}
	return nil
}

type VisualAssetLoader interface {
	Preload(context.Context, VisualAsset) error
	Unload(VisualAsset) error
}

type VisualPlanCacheOptions struct {
	Resolver           VisualAssetResolver
	Trust              VisualCatalogTrust
	Loader             VisualAssetLoader
	RequireTrust       bool
	PreloadConcurrency int
	MaxIdlePlans       int
}

type visualCacheEntry struct {
	ready       chan struct{}
	resolved    ResolvedPresentationPlan
	err         error
	refs        int
	lastUsed    time.Time
	fingerprint string
}

type VisualPlanCache struct {
	mutex        sync.Mutex
	resolver     VisualAssetResolver
	trust        VisualCatalogTrust
	loader       VisualAssetLoader
	requireTrust bool
	concurrency  int
	maxIdle      int
	entries      map[string]*visualCacheEntry
}

type VisualPlanLease struct {
	cache    *VisualPlanCache
	digest   string
	resolved ResolvedPresentationPlan
	released atomic.Bool
}

func NewVisualPlanCache(options VisualPlanCacheOptions) (*VisualPlanCache, error) {
	if options.Resolver == nil {
		return nil, ErrVisualAssetMissing
	}
	if options.Loader == nil {
		return nil, ErrVisualLoaderRequired
	}
	if options.RequireTrust && options.Trust == nil {
		return nil, ErrVisualCatalogUntrusted
	}
	if options.PreloadConcurrency <= 0 {
		options.PreloadConcurrency = 4
	}
	if options.MaxIdlePlans < 0 {
		options.MaxIdlePlans = 0
	}
	return &VisualPlanCache{resolver: options.Resolver, trust: options.Trust, loader: options.Loader, requireTrust: options.RequireTrust, concurrency: options.PreloadConcurrency, maxIdle: options.MaxIdlePlans, entries: make(map[string]*visualCacheEntry)}, nil
}

func (cache *VisualPlanCache) Acquire(ctx context.Context, plan PresentationPlan) (*VisualPlanLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	digest := plan.Identity.PresentationDigest
	if digest == "" {
		return nil, ErrVisualAssetMissing
	}
	fingerprint, err := presentationPlanFingerprint(plan)
	if err != nil {
		return nil, err
	}
	cache.mutex.Lock()
	entry := cache.entries[digest]
	if entry == nil {
		entry = &visualCacheEntry{ready: make(chan struct{}), fingerprint: fingerprint}
		cache.entries[digest] = entry
		cache.mutex.Unlock()
		cache.load(ctx, entry, plan)
	} else {
		if entry.fingerprint != fingerprint {
			cache.mutex.Unlock()
			return nil, ErrVisualDigestCollision
		}
		cache.mutex.Unlock()
	}
	select {
	case <-entry.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if entry.err != nil {
		return nil, entry.err
	}
	entry.refs++
	entry.lastUsed = time.Now()
	return &VisualPlanLease{cache: cache, digest: digest, resolved: cloneResolvedPlan(entry.resolved)}, nil
}

func (cache *VisualPlanCache) load(ctx context.Context, entry *visualCacheEntry, plan PresentationPlan) {
	var resolved ResolvedPresentationPlan
	var err error
	if cache.requireTrust || cache.trust != nil {
		if cache.trust == nil {
			err = ErrVisualCatalogUntrusted
		} else {
			err = cache.trust.VerifyCatalog(plan.Manifest.CatalogRevision, plan.Manifest.CatalogDigest)
		}
	}
	if err == nil {
		resolved, err = ResolvePresentationPlan(plan, cache.resolver)
	}
	if err == nil {
		err = cache.preload(ctx, &resolved)
	}
	cache.mutex.Lock()
	entry.resolved, entry.err, entry.lastUsed = resolved, err, time.Now()
	close(entry.ready)
	if err != nil {
		delete(cache.entries, plan.Identity.PresentationDigest)
	}
	cache.mutex.Unlock()
}

func (cache *VisualPlanCache) preload(ctx context.Context, resolved *ResolvedPresentationPlan) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, cache.concurrency)
	errorsChannel := make(chan error, len(resolved.Assets))
	loaded := make(chan VisualAsset, len(resolved.Assets))
	var wait sync.WaitGroup
	for index := range resolved.Assets {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				errorsChannel <- ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			asset := resolved.Assets[index].Asset
			err := cache.loader.Preload(ctx, asset)
			if err != nil && asset.FallbackKey != "" {
				asset.Key, asset.FallbackKey = asset.FallbackKey, ""
				err = cache.loader.Preload(ctx, asset)
				if err == nil {
					resolved.Assets[index].Asset = asset
				}
			}
			if err != nil {
				errorsChannel <- fmt.Errorf("preload %q: %w", asset.Key, err)
				cancel()
				return
			}
			loaded <- asset
		}()
	}
	wait.Wait()
	close(errorsChannel)
	close(loaded)
	var first error
	for err := range errorsChannel {
		if first == nil {
			first = err
		}
	}
	if first != nil {
		var cleanupErrors []error
		for asset := range loaded {
			if err := cache.loader.Unload(asset); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		return errors.Join(append([]error{first}, cleanupErrors...)...)
	}
	return nil
}

func (lease *VisualPlanLease) Plan() ResolvedPresentationPlan {
	if lease == nil {
		return ResolvedPresentationPlan{}
	}
	return cloneResolvedPlan(lease.resolved)
}
func (lease *VisualPlanLease) Release() error {
	if lease == nil || !lease.released.CompareAndSwap(false, true) {
		return nil
	}
	return lease.cache.release(lease.digest)
}

func (cache *VisualPlanCache) release(digest string) error {
	cache.mutex.Lock()
	entry := cache.entries[digest]
	if entry == nil {
		cache.mutex.Unlock()
		return nil
	}
	if entry.refs > 0 {
		entry.refs--
	}
	entry.lastUsed = time.Now()
	var evicted []ResolvedPresentationPlan
	for idleCount(cache.entries) > cache.maxIdle {
		key, candidate := oldestIdle(cache.entries)
		if candidate == nil {
			break
		}
		delete(cache.entries, key)
		evicted = append(evicted, candidate.resolved)
	}
	cache.mutex.Unlock()
	return cache.unloadPlans(evicted)
}

func (cache *VisualPlanCache) InvalidateCatalog(revision, digest string) error {
	cache.mutex.Lock()
	var evicted []ResolvedPresentationPlan
	for key, entry := range cache.entries {
		if entry.resolved.Plan.Manifest.CatalogRevision == revision && entry.resolved.Plan.Manifest.CatalogDigest == digest {
			if entry.refs != 0 {
				cache.mutex.Unlock()
				return ErrVisualAssetInUse
			}
			delete(cache.entries, key)
			evicted = append(evicted, entry.resolved)
		}
	}
	cache.mutex.Unlock()
	return cache.unloadPlans(evicted)
}

func (cache *VisualPlanCache) unloadPlans(plans []ResolvedPresentationPlan) error {
	var unloadErrors []error
	for _, plan := range plans {
		for _, value := range plan.Assets {
			if err := cache.loader.Unload(value.Asset); err != nil {
				unloadErrors = append(unloadErrors, fmt.Errorf("unload %q: %w", value.Asset.Key, err))
			}
		}
	}
	return errors.Join(unloadErrors...)
}

func presentationPlanFingerprint(plan PresentationPlan) (string, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func idleCount(values map[string]*visualCacheEntry) int {
	count := 0
	for _, value := range values {
		if value.refs == 0 && value.err == nil {
			count++
		}
	}
	return count
}
func oldestIdle(values map[string]*visualCacheEntry) (string, *visualCacheEntry) {
	var key string
	var result *visualCacheEntry
	for candidateKey, value := range values {
		if value.refs != 0 || value.err != nil {
			continue
		}
		if result == nil || value.lastUsed.Before(result.lastUsed) {
			key, result = candidateKey, value
		}
	}
	return key, result
}
func cloneResolvedPlan(value ResolvedPresentationPlan) ResolvedPresentationPlan {
	value.Plan = clonePresentationPlan(value.Plan)
	value.Assets = append([]ResolvedVisualAsset(nil), value.Assets...)
	for index := range value.Assets {
		value.Assets[index].Visual = cloneVisualView(value.Assets[index].Visual)
		value.Assets[index].Asset.Preload = append([]string(nil), value.Assets[index].Asset.Preload...)
	}
	return value
}
