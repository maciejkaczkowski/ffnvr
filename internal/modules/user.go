package modules

import (
	"errors"
	"fmt"

	"ffnvr/internal/config"
	"ffnvr/internal/pwhash"
	"ffnvr/internal/script"
)

// userModule: /user/group, /user/policy and /user - accounts of the web panel.
//
//	/user/group
//	add group="firma1"
//	/user/policy
//	add name=viewer allow="live,archive"
//	/user
//	add user="maciej" groups="firma1,firma2" password-hash="\$6\$..."
//	add user="jan" groups="firma2" policy=viewer password="secret"
//
// groups= (with valid-users of /nvr) chooses the recorders a user sees, policy= what
// the user may do with them (see policy.go).
//
// password= is never kept: it becomes password-hash (SHA-512 crypt, as in /etc/shadow)
// the moment it is given, also when a configuration file is loaded, so export and
// save only ever write the hash.
type userModule struct{}

func (userModule) Name() string { return "user" }

func (userModule) Menus() []*config.Menu {
	return []*config.Menu{
		{
			Path:    "/user/group",
			Kind:    config.Multi,
			NameKey: "group",
			Help:    "user groups (e.g. one per company)",
			Fields: []config.Field{
				{Name: "group", Required: true},
				{Name: "comment"},
			},
			Validate: func(_ *config.Store, it *config.Item) error { return checkName("group", it.Val("group")) },
		},
		policyMenu(),
		{
			Path:    "/user",
			Kind:    config.Multi,
			NameKey: "user",
			Help:    "users of the web panel",
			Fields: []config.Field{
				{Name: "user", Required: true},
				{Name: "groups", Type: config.TList, Ref: "/user/group:group", Strict: true},
				{Name: "policy", Ref: "/user/policy:name", Strict: true, Help: "rights in the web panel; none: no rights"},
				{Name: "password", Secret: true, Help: "stored only as password-hash"},
				{Name: "password-hash", Secret: true, Help: `crypt hash: $6$... (SHA-512) or $5$... (SHA-256)`},
				{Name: "disabled", Type: config.TBool},
				{Name: "comment"},
			},
			CheckParams: func(ps []script.Param) error {
				var pw, hash bool
				for _, p := range ps {
					pw = pw || p.Key == "password"
					hash = hash || p.Key == "password-hash"
				}
				if pw && hash {
					return errors.New("give password or password-hash, not both")
				}
				return nil
			},
			Validate: validateUser,
			Commands: map[string]config.Command{
				"rights": {Help: "show what a user may do in the web panel", Run: userRightsCommand},
			},
		},
	}
}

func validateUser(_ *config.Store, it *config.Item) error {
	if err := checkName("user", it.Val("user")); err != nil {
		return err
	}
	if pw := it.Val("password"); pw != "" {
		h, err := pwhash.Hash(pw)
		if err != nil {
			return err
		}
		it.Set("password-hash", h)
	}
	it.Unset("password") // never kept
	h := it.Val("password-hash")
	if h == "" {
		return errors.New("password or password-hash is required")
	}
	if err := pwhash.Valid(h); err != nil {
		return fmt.Errorf("password-hash: %w", err)
	}
	return nil
}

// Authenticate checks a login of the web panel: the user must exist, be enabled and
// the password match. It returns the user's groups.
func Authenticate(s *config.Store, user, password string) (groups []string, ok bool) {
	u := find(s, "/user", "user", user)
	if u == nil || u.Val("disabled") == "yes" || !pwhash.Verify(password, u.Val("password-hash")) {
		return nil, false
	}
	if g := u.Val("groups"); g != "" {
		groups = splitList(g)
	}
	return groups, true
}

// CanAccess tells whether a user may use a recorder in the web panel: the recorder's
// valid-users names the user or one of the user's groups (@group). Without valid-users
// nobody may; a disabled user never.
func CanAccess(s *config.Store, user, nvr string) bool {
	u := find(s, "/user", "user", user)
	n := find(s, "/nvr", "name", nvr)
	if u == nil || n == nil || u.Val("disabled") == "yes" {
		return false
	}
	groups := map[string]bool{}
	for _, g := range splitList(u.Val("groups")) {
		groups[g] = true
	}
	for _, v := range splitList(n.Val("valid-users")) {
		if v == user || (len(v) > 1 && v[0] == '@' && groups[v[1:]]) {
			return true
		}
	}
	return false
}

// AccessibleNvrs lists the recorders a user may use.
func AccessibleNvrs(s *config.Store, user string) []string {
	var out []string
	for _, n := range s.Items("/nvr") {
		if CanAccess(s, user, n.Val("name")) {
			out = append(out, n.Val("name"))
		}
	}
	return out
}

func splitList(v string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == ',' {
			if i > start {
				out = append(out, v[start:i])
			}
			start = i + 1
		}
	}
	return out
}
