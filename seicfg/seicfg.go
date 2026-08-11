// Package seicfg holds a seid node's configuration as one resolved value.
//
// A node's configuration reaches running code through two channels: a Tendermint
// config struct and a flat key-value map that callers index by string and cast at
// the read site. Config carries both, so a caller takes one value rather than two,
// and so a second resolver can be compared against the legacy one by comparing
// values of a single type.
package seicfg

import (
	serverconfig "github.com/sei-protocol/sei-chain/sei-cosmos/server/config"
	servertypes "github.com/sei-protocol/sei-chain/sei-cosmos/server/types"
	tmcfg "github.com/sei-protocol/sei-chain/sei-tendermint/config"
)

// Config is a seid node's configuration after resolution.
//
// App and Tendermint are named fields rather than embedded ones. Both underlying
// types are called Config and both embed a BaseConfig, so embedding the pair does
// not compile; promoting their fields onto this type has to be done by hand, one
// key at a time, which is also where a key that both files declare gets resolved
// rather than shadowed by depth.
type Config struct {
	// App is the app.toml surface.
	App serverconfig.Config

	// Tendermint is the config.toml surface.
	Tendermint *tmcfg.Config

	// flat is the key-value view reads take until they move onto a field above.
	flat servertypes.AppOptions
}

// FromLegacy assembles a Config from what the legacy interception handler resolved.
//
// It only gathers what the caller already holds, so it cannot resolve differently
// from the handler that produced its arguments. That is what makes introducing this
// type a change in shape rather than in behaviour.
func FromLegacy(app serverconfig.Config, tendermint *tmcfg.Config, flat servertypes.AppOptions) Config {
	return Config{App: app, Tendermint: tendermint, flat: flat}
}

// AppOptions returns the flat key-value view of this configuration.
//
// The returned value is the one FromLegacy was given, so a read through it resolves
// exactly as it did before this type existed. Each read that moves onto a Config
// field retires one caller of this method, and the method goes when the last one does.
func (c Config) AppOptions() servertypes.AppOptions {
	return c.flat
}
