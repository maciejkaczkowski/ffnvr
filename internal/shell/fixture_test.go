package shell

import (
	"testing"

	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/module"
)

// testModule provides menus of every kind for the shell tests; the real modules are not needed
// to exercise navigation, completion, help and save/load.
type testModule struct{}

func (testModule) Name() string { return "test" }

func (testModule) Menus() []*config.Menu {
	return []*config.Menu{
		{Path: "/system/clock", Kind: config.Single, Help: "time zone", Fields: []config.Field{
			{Name: "time-zone-name", Ref: "sys:timezones", Help: "e.g. Europe/Warsaw"},
		}},
		{Path: "/system/identity", Kind: config.Single, Fields: []config.Field{{Name: "name"}}},
		{Path: "/camera", Kind: config.Multi, NameKey: "name", Help: "cameras", Fields: []config.Field{
			{Name: "name"},
			{Name: "url"},
			{Name: "interface", Ref: "sys:interfaces"},
			{Name: "transport", Type: config.TEnum, Enum: []string{"tcp", "udp", "auto"}},
			{Name: "events", Type: config.TEnumList, Enum: []string{"motion", "tamper", "line"}},
			{Name: "fps", Type: config.TInt},
			{Name: "password", Secret: true},
			{Name: "disabled", Type: config.TBool},
			{Name: "comment"},
		}},
		{Path: "/camera/group", Kind: config.Multi, NameKey: "name", Fields: []config.Field{
			{Name: "name"},
			{Name: "cameras", Type: config.TList, Ref: "/camera:name"},
		}},
		{Path: "/record", Kind: config.Multi, Aliases: []string{"/recording"},
			Settings: []config.Field{{Name: "enabled", Type: config.TBool}, {Name: "path"}},
			Fields: []config.Field{
				{Name: "camera", Ref: "/camera:name"},
				{Name: "mode", Type: config.TEnum, Enum: []string{"continuous", "events"}},
				{Name: "retention"},
			}},
		{Path: "/log", Kind: config.Multi, Fields: []config.Field{{Name: "topic"}, {Name: "comment"}}},
	}
}

func newEngine(t *testing.T, root string) *engine.Engine {
	t.Helper()
	e, err := engine.New(engine.Options{Root: root, Modules: []module.Module{testModule{}}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
