package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// TenantHosting is the hardening of users' containers that needs the host's setup
// (NUDGE_TENANT_*, docs/config/server.md "Hosting other users"). Admins' containers get
// none of it.
type TenantHosting struct {
	// CgroupParent is the cgroup parent of a user's containers, with {id} replaced by the
	// user's ID (e.g. nudge-u{id}.slice); empty leaves Docker's default.
	CgroupParent string
	// Runtime is the OCI runtime of a user's containers (e.g. runsc); empty leaves the
	// daemon's default. A daemon that doesn't list it creates and starts none.
	Runtime string
	// StorageSize caps the writable layer of a user's container in bytes, where the
	// daemon's storage driver supports it; zero means no cap.
	StorageSize int64
}

// hostSettings sets h on hc, the host config of a container of owner (nothing for an
// admin's container).
func (h TenantHosting) hostSettings(hc *container.HostConfig, owner tenantOwner) {
	if owner.key == "" {
		return
	}
	if h.CgroupParent != "" {
		hc.CgroupParent = strings.ReplaceAll(h.CgroupParent, "{id}", strconv.FormatInt(owner.id, 10))
	}
	if h.Runtime != "" {
		hc.Runtime = h.Runtime
	}
	if h.StorageSize > 0 {
		hc.StorageOpt = map[string]string{"size": strconv.FormatInt(h.StorageSize, 10)}
	}
}

// errNoRuntime refuses a user's container on a daemon without the configured runtime: it
// would run unsandboxed or not at all.
func errNoRuntime(name string) error {
	return &statusError{code: http.StatusConflict, msg: fmt.Sprintf("this environment can't run users' containers: its Docker daemon has no runtime %q", name)}
}

// tenantHosting returns the hardening for a container of owner on c's daemon (none for an
// admin's): refused if the daemon doesn't list the runtime, without the disk cap if its
// storage driver can't set one (logged).
func (s *server) tenantHosting(ctx context.Context, c docker.Client, owner tenantOwner) (TenantHosting, error) {
	if owner.key == "" {
		return TenantHosting{}, nil
	}
	h, driver, err := s.daemonHosting(ctx, c)
	if err != nil {
		return TenantHosting{}, err
	}
	if s.tenant.StorageSize > 0 && h.StorageSize == 0 {
		s.log.Warn("the storage driver can't limit a container's disk: users' containers get no disk cap", "driver", driver)
	}
	return h, nil
}

// daemonHosting returns the hardening c's daemon gives users' containers and its storage
// driver: refused if it doesn't list the runtime, without the disk cap if its storage
// driver can't set one.
func (s *server) daemonHosting(ctx context.Context, c docker.Client) (TenantHosting, string, error) {
	h := s.tenant
	if h.Runtime == "" && h.StorageSize == 0 {
		return h, "", nil
	}
	res, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		return TenantHosting{}, "", &dockerFailure{msg: "docker info", err: err}
	}
	if h.Runtime != "" {
		if _, ok := res.Info.Runtimes[h.Runtime]; !ok {
			return TenantHosting{}, "", errNoRuntime(h.Runtime)
		}
	}
	if h.StorageSize > 0 && !storageSizeSupported(res.Info) {
		h.StorageSize = 0
	}
	return h, res.Info.Driver, nil
}

// storageSizeSupported reports whether the daemon's storage driver takes a container's
// storage_opt size: overlay2 only on xfs (which must also be mounted with pquota, which
// the daemon doesn't report; it refuses the create without), and the drivers that always
// do.
func storageSizeSupported(info system.Info) bool {
	switch info.Driver {
	case "overlay2":
		for _, kv := range info.DriverStatus {
			if kv[0] == "Backing Filesystem" {
				return kv[1] == "xfs"
			}
		}
		return false
	case "btrfs", "zfs", "devicemapper", "windowsfilter":
		return true
	}
	return false
}

// checkStartHosting refuses to start a user's container (owner key != "") on a daemon
// without the configured runtime, or that was made with other hosting than a user's
// container gets now (before a setting was configured or changed): its runtime, cgroup
// parent and, where the storage driver can set one, disk cap. Recreating it builds it
// with the current ones.
func (s *server) checkStartHosting(ctx context.Context, c docker.Client, key string, hc *container.HostConfig) error {
	if key == "" || s.tenant == (TenantHosting{}) {
		return nil
	}
	owner, err := s.tenantOwnerOf(ctx, key)
	if err != nil {
		return err
	}
	h, _, err := s.daemonHosting(ctx, c)
	if err != nil {
		return err
	}
	var want container.HostConfig
	h.hostSettings(&want, owner)
	var stale string
	switch {
	case h.Runtime != "" && hc.Runtime != want.Runtime:
		stale = fmt.Sprintf("run with runtime %q", h.Runtime)
	case h.CgroupParent != "" && hc.CgroupParent != want.CgroupParent:
		stale = fmt.Sprintf("run in cgroup parent %q", want.CgroupParent)
	case h.StorageSize > 0 && hc.StorageOpt["size"] != want.StorageOpt["size"]:
		stale = fmt.Sprintf("have a disk cap of %d bytes", h.StorageSize)
	default:
		return nil
	}
	return &statusError{code: http.StatusConflict, msg: "users' containers " + stale + " here: recreate the container to start it"}
}
