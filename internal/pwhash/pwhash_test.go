package pwhash

import (
	"strings"
	"testing"
)

func TestCrypt(t *testing.T) {
	for _, c := range []struct{ key, setting, want string }{
		// from the specification and openssl passwd
		{"Hello world!", "$6$saltstring", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"Hello world!", "$5$saltstring", "$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5"},
		{"secret", "$6$qznl79kPr.SktcO6", "$6$qznl79kPr.SktcO6$tJeWRY5qPMyhc0rlkaadFmSFYFrmikq48rTrck9DkZ79epo1tCu2/BdMTTAqI/DFoe34IBVfgqb2bvnP3uRkF1"},
		{"Hello world!", "$6$rounds=10000$saltstringsaltstring", "$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
		{"This is just a test", "$5$rounds=5000$toolongsaltstring", "$5$rounds=5000$toolongsaltstrin$Un/5jzAHMgOGZ5.mWJpuVolil07guHPvOW8mGRcvxa5"},
	} {
		got, err := Crypt(c.key, c.setting)
		if err != nil || got != c.want {
			t.Errorf("%q %s:\n got %s %v\nwant %s", c.key, c.setting, got, err, c.want)
		}
		if !Verify(c.key, c.want) || Verify(c.key+"x", c.want) {
			t.Errorf("verify %s", c.want)
		}
	}
}

func TestHashAndValid(t *testing.T) {
	h, err := Hash("secret")
	if err != nil || !strings.HasPrefix(h, "$6$") || len(h) != 3+16+1+86 || !Verify("secret", h) || Valid(h) != nil {
		t.Errorf("%s %v", h, err)
	}
	if h2, _ := Hash("secret"); h2 == h {
		t.Error("salts must differ")
	}
	for _, bad := range []string{"", "plain", "$1$abc$xyz", "$apr1$43Q55M8k$short", "$apr1$toolongsalt$814QZXP//txlmYR3qslcO.", "$apr1$$814QZXP//txlmYR3qslcO.", "$apr1$43Q55M8k", "$6$salt$short", "$6$salt$" + strings.Repeat("!", 86), "$6$rounds=x$s$h"} {
		if Valid(bad) == nil {
			t.Errorf("%q must be invalid", bad)
		}
	}
	if Valid("$6$qznl79kPr.SktcO6$M8Kz7/WsqZstuCcZ5w7/hOo/2vXFuM3bbWpbPd86izZRSuh9aL1lc6qiAYkGw1m8aGws4r01bpIzkqOwGMzbQ/") != nil {
		t.Error("the hash from the request is valid")
	}
}

func TestMD5Refused(t *testing.T) {
	// MD5-crypt (htpasswd -m, /etc/shadow $1$) is not accepted
	for _, h := range []string{"$apr1$43Q55M8k$65Fd66gB.xBa46j6kFl7v.", "$1$saltstri$YMyguxXMBpd2TEZ.vS/3q1"} {
		if Valid(h) == nil || Verify("test", h) || Verify("Hello world!", h) {
			t.Errorf("%s must be refused", h)
		}
	}
}
