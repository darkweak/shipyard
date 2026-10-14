package gitx

import "testing"

func TestValidateURL(t *testing.T) {
	ok := []string{"https://github.com/a/b.git", "ssh://git@host:22/a/b", "git@github.com:a/b.git"}
	bad := []string{"", "file:///etc", "ext::sh -c id", "-uhttps://x", "/local/repo", "git@host:-oProxyCommand=x"}
	for _, u := range ok {
		if err := ValidateURL(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	for _, u := range bad {
		if ValidateURL(u) == nil {
			t.Errorf("%q: expected error", u)
		}
	}
}

func TestValidateBranch(t *testing.T) {
	for _, b := range []string{"main", "release/1.2", "feat_x-y"} {
		if err := ValidateBranch(b); err != nil {
			t.Errorf("%q: %v", b, err)
		}
	}
	for _, b := range []string{"", "-x", "a..b", "a b", "a;b"} {
		if ValidateBranch(b) == nil {
			t.Errorf("%q: expected error", b)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	if _, err := SafeJoin("/repo", "../etc"); err == nil {
		t.Error("expected escape to be refused")
	}
	if p, err := SafeJoin("/repo", "apps/web"); err != nil || p != "/repo/apps/web" {
		t.Errorf("got %q, %v", p, err)
	}
}
