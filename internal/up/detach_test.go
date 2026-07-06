package up

import (
	"testing"

	"github.com/liu1700/gw/internal/branchinfo"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/registry"
)

// crashedServices reports services whose route is absent — i.e. they exited
// after being spawned. A registered service is healthy; a missing one crashed.
func TestCrashedServices(t *testing.T) {
	t.Setenv("GW_STATE_DIR", t.TempDir())

	cfg := &config.Config{
		Domain: "app.localhost",
		Services: []config.Service{
			{Name: "web"},
			{Name: "api"},
		},
	}
	info := branchinfo.Info{Branch: "main", Slug: "main", IsMain: true}

	// Only "web" is registered; "api" never came up (or crashed and was dropped).
	webHost := cfg.HostFor("web", info.Slug, info.IsMain)
	if err := registry.Register(registry.Route{Host: webHost, Port: 1234, Service: "web", Branch: "main"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := crashedServices(cfg, info)
	if len(got) != 1 || got[0] != "api" {
		t.Fatalf("crashedServices = %v, want [api]", got)
	}

	// Once "api" registers too, nothing is crashed.
	apiHost := cfg.HostFor("api", info.Slug, info.IsMain)
	if err := registry.Register(registry.Route{Host: apiHost, Port: 5678, Service: "api", Branch: "main"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := crashedServices(cfg, info); len(got) != 0 {
		t.Fatalf("crashedServices = %v, want []", got)
	}
}
