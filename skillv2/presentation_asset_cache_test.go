package skillv2

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type cacheVisualResolver struct{}

func (cacheVisualResolver) CatalogIdentity() (string, string) { return "r1", "d1" }
func (cacheVisualResolver) Resolve(visual VisualView) (VisualAsset, error) {
	return VisualAsset{Key: "missing", FallbackKey: "fallback", Preload: visual.Elements}, nil
}

type cacheVisualLoader struct {
	mutex    sync.Mutex
	loaded   []string
	unloaded []string
}

func (loader *cacheVisualLoader) Preload(_ context.Context, asset VisualAsset) error {
	loader.mutex.Lock()
	defer loader.mutex.Unlock()
	loader.loaded = append(loader.loaded, asset.Key)
	if asset.Key == "missing" {
		return errors.New("not installed")
	}
	return nil
}
func (loader *cacheVisualLoader) Unload(asset VisualAsset) error {
	loader.mutex.Lock()
	defer loader.mutex.Unlock()
	loader.unloaded = append(loader.unloaded, asset.Key)
	return nil
}

func TestVisualPlanCacheTrustFallbackAndRelease(t *testing.T) {
	loader := &cacheVisualLoader{}
	cache, err := NewVisualPlanCache(VisualPlanCacheOptions{Resolver: cacheVisualResolver{}, Trust: TrustedVisualCatalogs{"r1": "d1"}, Loader: loader, RequireTrust: true, MaxIdlePlans: 0})
	if err != nil {
		t.Fatal(err)
	}
	plan := PresentationPlan{Identity: ProgramIdentityView{PresentationDigest: "plan-1"}, Manifest: SkillVisualManifest{CatalogRevision: "r1", CatalogDigest: "d1", Entries: []VisualView{{Index: 0, Category: "impact", Theme: "default", Elements: []string{"fire"}}}}, Effects: []PresentationMount{{VisualIndex: 0}}}
	lease, err := cache.Acquire(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := lease.Plan().Assets[0].Asset.Key; got != "fallback" {
		t.Fatalf("asset key = %q", got)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	loader.mutex.Lock()
	defer loader.mutex.Unlock()
	if len(loader.unloaded) != 1 || loader.unloaded[0] != "fallback" {
		t.Fatalf("unloaded = %v", loader.unloaded)
	}
}

func TestVisualPlanCacheRejectsUntrustedCatalog(t *testing.T) {
	cache, err := NewVisualPlanCache(VisualPlanCacheOptions{Resolver: cacheVisualResolver{}, Trust: TrustedVisualCatalogs{"r1": "other"}, Loader: &cacheVisualLoader{}, RequireTrust: true})
	if err != nil {
		t.Fatal(err)
	}
	plan := PresentationPlan{Identity: ProgramIdentityView{PresentationDigest: "plan-1"}, Manifest: SkillVisualManifest{CatalogRevision: "r1", CatalogDigest: "d1"}}
	if _, err := cache.Acquire(context.Background(), plan); !errors.Is(err, ErrVisualCatalogUntrusted) {
		t.Fatalf("trust error = %v", err)
	}
}

func TestVisualPlanCacheRejectsDigestCollision(t *testing.T) {
	cache, err := NewVisualPlanCache(VisualPlanCacheOptions{Resolver: cacheVisualResolver{}, Loader: &cacheVisualLoader{}, MaxIdlePlans: 1})
	if err != nil {
		t.Fatal(err)
	}
	plan := PresentationPlan{Identity: ProgramIdentityView{PresentationDigest: "same"}, Manifest: SkillVisualManifest{CatalogRevision: "r1", CatalogDigest: "d1"}}
	lease, err := cache.Acquire(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.Manifest.CatalogDigest = "different"
	if _, err := cache.Acquire(context.Background(), changed); !errors.Is(err, ErrVisualDigestCollision) {
		t.Fatalf("collision error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}
