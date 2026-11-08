// Package module defines the interface implemented by feature modules
// (cameras, recording, storage...).
package module

import (
	"ffnvr/internal/config"
	"ffnvr/internal/script"
	"ffnvr/internal/units"
)

// Context is passed to modules that need more than their menus.
type Context struct {
	Store *config.Store
	// Root is the filesystem root ("/" on the real system); completion sources and
	// live checks read files below it.
	Root string
	// Live is true when ffnvr works on the real system (root "/").
	Live bool
	// Binary identifies the ffnvr binary the services run (a hash of it, set when units
	// are planned): put into the units of ffnvr's own services, a new version changes
	// them, so commit restarts them.
	Binary string
}

// Module groups the RouterOS-style menus of one feature.
type Module interface {
	Name() string
	Menus() []*config.Menu
}

// Describer is optionally implemented by a module to add live information to "print" of its
// list menus: one line per item ("" for none), e.g. the state of a camera stream. nil = nothing to add.
type Describer interface {
	Describe(c *Context, menu string) []string
}

// UnitBuilder is implemented by modules that run services: it returns the systemd units
// wanted by the current configuration (see package units).
type UnitBuilder interface {
	Units(c *Context) ([]units.Unit, error)
}

// Binder is implemented by modules that need the context outside of the calls that
// receive it (e.g. action menus): Bind is called once, after all menus are registered.
type Binder interface {
	Bind(c *Context)
}

// CommandHook is implemented by modules that follow up interactive commands (see
// engine.AfterCommand).
type CommandHook interface {
	AfterCommand(c *Context, cmd script.Command) string
}
