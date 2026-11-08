package fsusage

import "testing"

func TestOf(t *testing.T) {
	u, err := Of(t.TempDir())
	if err != nil || u.Total == 0 || u.Free > u.Total || u.Percent() < 0 || u.Percent() > 100 {
		t.Errorf("%+v %v", u, err)
	}
	if _, err := Of("/no/such/dir"); err == nil {
		t.Error("missing path")
	}
}
