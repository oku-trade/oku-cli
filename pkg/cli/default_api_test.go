package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oku-trade/oku-cli/pkg/config"
)

func TestDefaultAPIURLIsProduction(t *testing.T) {
	if config.DefaultAPIURL != "https://accounts.icarus.tools" || config.StagingAPIURL != "https://accounts.lat.gfx.town" {
		t.Fatalf("defaults: %s %s", config.DefaultAPIURL, config.StagingAPIURL)
	}
}

// A fresh profile created without --api-url gets production; an existing
// profile that stored the staging URL keeps it after the default changed.
func TestFreshProfileProdExistingStagingUnchanged(t *testing.T) {
	h := newHarness(t)
	const fresh = "0x1111111111111111111111111111111111111111"
	if r := h.run("profiles", "add", "fresh", "--account", fresh, "--json"); r.code != 0 {
		t.Fatalf("add fresh: %s %s", r.stdout, r.stderr)
	}
	cfg := filepath.Join(os.Getenv("OKU_CONFIG_DIR"), "config.json")
	// Simulate a config written before the default changed.
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.Profiles["fresh"].APIURL != config.DefaultAPIURL {
		t.Fatalf("fresh profile api_url %q in %s", f.Profiles["fresh"].APIURL, raw)
	}
	f.Profiles["old"] = &config.Profile{Network: config.NetworkMainnet, APIURL: config.StagingAPIURL, Account: "0x2222222222222222222222222222222222222222"}
	if err = f.Save(); err != nil {
		t.Fatal(err)
	}
	r := h.run("profiles", "list", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, config.StagingAPIURL) || !strings.Contains(r.stdout, config.DefaultAPIURL) {
		t.Fatalf("existing staging profile must keep its URL: %s", r.stdout)
	}
	after, err := config.Load()
	if err != nil || after.Profiles["old"].APIURL != config.StagingAPIURL || after.Profiles["fresh"].APIURL != config.DefaultAPIURL {
		t.Fatalf("profiles after CLI run: %+v %v", after.Profiles, err)
	}
}
