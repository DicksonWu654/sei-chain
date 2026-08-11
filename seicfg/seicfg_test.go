package seicfg_test

import (
	"testing"

	"github.com/spf13/viper"

	"github.com/sei-protocol/sei-chain/sei-cosmos/server"
	serverconfig "github.com/sei-protocol/sei-chain/sei-cosmos/server/config"
	"github.com/sei-protocol/sei-chain/sei-tendermint/config"
	"github.com/sei-protocol/sei-chain/seicfg"
)

// This type exists to change the shape of the boot's configuration argument without
// changing what any read resolves to, so what these tests hold is that it stays a
// change in shape. They are the anchor for every later commit that moves one read off
// AppOptions and onto a Config field: while this file passes, a read that has not
// moved yet cannot have been affected by the ones that have.
//
// The identity assertions below are deliberately stronger than equality. A Config that
// rebuilt or copied the flat view would still satisfy an equality check on the keys a
// test happened to set, and would silently drop whatever the copy did not carry. Nothing
// short of handing back the same value rules that out.

// TestAppOptionsIsTheValueItWasGiven pins that the flat view survives assembly untouched.
func TestAppOptionsIsTheValueItWasGiven(t *testing.T) {
	flat := viper.New()
	flat.Set(server.FlagMinRetainBlocks, "100000")

	cfg := seicfg.FromLegacy(*serverconfig.DefaultConfig(), config.DefaultConfig(), flat)

	if got := cfg.AppOptions(); got != flat {
		t.Fatalf("AppOptions returned %#v rather than the value FromLegacy was given. Any read that "+
			"has not moved onto a Config field goes through this value, so a Config that rebuilds or "+
			"copies it resolves those reads against something the legacy handler never produced", got)
	}
}

// TestAppOptionsResolvesEveryKeyTheSameWay holds the flat view against the value it wraps,
// key by key, so a read through Config cannot differ from the same read before it existed.
func TestAppOptionsResolvesEveryKeyTheSameWay(t *testing.T) {
	flat := viper.New()
	// Shapes the legacy path actually produces: a string from env or quoted TOML, an int64
	// from an unquoted TOML integer, a float64 from a TOML float, and a nested key. The
	// float is here because its Go type is what made one receipt read architecture
	// dependent, so a view that normalised types would be caught by this row first.
	flat.Set(server.FlagMinRetainBlocks, "100000")
	flat.Set("state-store.ss-keep-recent", int64(400000))
	flat.Set("evm.max_log_bytes", float64(1e19))
	flat.Set(server.FlagMinGasPrices, "0.01usei")

	cfg := seicfg.FromLegacy(*serverconfig.DefaultConfig(), config.DefaultConfig(), flat)

	for _, key := range flat.AllKeys() {
		want, got := flat.Get(key), cfg.AppOptions().Get(key)
		if want != got {
			t.Errorf("%s resolves to %#v (%T) through Config and %#v (%T) directly. The two have to "+
				"agree for this type to be a change in shape rather than in behaviour, and a type "+
				"difference alone is enough to change what a cast at the read site produces",
				key, got, got, want, want)
		}
	}
}

// TestTendermintIsTheConfigItWasGiven pins the other channel. The boot hands this struct
// straight to the app, so a copy here would let a later mutation of the original go
// unseen by the app, or the reverse.
func TestTendermintIsTheConfigItWasGiven(t *testing.T) {
	tm := config.DefaultConfig()

	cfg := seicfg.FromLegacy(*serverconfig.DefaultConfig(), tm, viper.New())

	if cfg.Tendermint != tm {
		t.Fatalf("Tendermint is a different *config.Config from the one FromLegacy was given. The "+
			"boot passes this pointer to the app, so a copy makes the app read a struct that stops "+
			"tracking the one the handler populated. got %p, want %p", cfg.Tendermint, tm)
	}
}

// TestAppCarriesTheResolvedAppConfig pins that the app.toml surface arrives whole, using a
// field no other assertion here touches.
func TestAppCarriesTheResolvedAppConfig(t *testing.T) {
	app := serverconfig.DefaultConfig()
	app.MinGasPrices = "0.02usei"
	app.Pruning = "custom"

	cfg := seicfg.FromLegacy(*app, config.DefaultConfig(), viper.New())

	if cfg.App.MinGasPrices != "0.02usei" || cfg.App.Pruning != "custom" {
		t.Fatalf("App resolved to MinGasPrices=%q Pruning=%q rather than the struct FromLegacy was "+
			"given. This is the surface later commits move reads onto, so it has to arrive as the "+
			"legacy handler resolved it", cfg.App.MinGasPrices, cfg.App.Pruning)
	}
}
