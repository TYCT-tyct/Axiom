package containertest

import (
	"os"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

func TestCgroupParent(t *testing.T) {
	for _, c := range []struct{ name, parent, want string }{
		{"empty", "", "developer.slice"},
		{"ci", "axiom-ci.slice", "axiom-ci.slice"},
		{"other host", "other-ci.slice", "other-ci.slice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("AXIOM_TEST_CGROUP_PARENT", c.parent)
			checkHostConfig(t, c.want)
		})
	}
}

func TestCgroupParentUnset(t *testing.T) {
	t.Setenv("AXIOM_TEST_CGROUP_PARENT", "")
	if err := os.Unsetenv("AXIOM_TEST_CGROUP_PARENT"); err != nil {
		t.Fatal(err)
	}
	checkHostConfig(t, "developer.slice")
}

func checkHostConfig(t *testing.T, want string) {
	t.Helper()
	var req testcontainers.GenericContainerRequest
	if err := testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
		host.CgroupParent = "developer.slice"
		host.ShmSize = 1234
	})(&req); err != nil {
		t.Fatal(err)
	}
	if err := WithCgroupParent()(&req); err != nil {
		t.Fatal(err)
	}
	var host container.HostConfig
	req.HostConfigModifier(&host)
	if host.CgroupParent != want || host.ShmSize != 1234 {
		t.Fatalf("host config = %+v, want parent %q and unchanged shared memory", host, want)
	}
}
