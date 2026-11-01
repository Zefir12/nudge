package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// A user's networks and volumes are created by nudge with their owner label and a name
// prefixed with their id ("u<id>-"), so they can't take or guess another user's name. Ids
// can come back after a user is deleted, so a name is theirs only if the owner label is:
// a leftover of a deleted user is a conflict, never handed over.

// userPrefix is the prefix of the names of user id's networks and volumes. The id ends at
// the dash, so no user's prefix starts another's.
func userPrefix(id int64) string { return "u" + strconv.FormatInt(id, 10) + "-" }

// defaultNetwork is the name of user id's default bridge network, created when they are
// given an environment.
func defaultNetwork(id int64) string { return "nudge-u-" + strconv.FormatInt(id, 10) }

// userBridgePrefix starts the Linux name of every user's network's bridge, so one host
// firewall rule (`-i nudge+`) matches all of them (docs/config/server.md, "Network").
const userBridgePrefix = "nudge"

// bridgeNameOption is the bridge driver option naming the network's Linux interface.
const bridgeNameOption = "com.docker.network.bridge.name"

// userBridgeName is the Linux interface name of the user's network named name (the full
// name, prefix included): userBridgePrefix and 10 hex digits of a hash of the name, 15
// characters, the most Linux allows. Names are unique on a daemon, so are their bridges'.
func userBridgeName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return userBridgePrefix + hex.EncodeToString(sum[:])[:10]
}

// userNetworkOptions are the driver options of the user's new network named name.
func userNetworkOptions(name string) map[string]string {
	return map[string]string{bridgeNameOption: userBridgeName(name)}
}

// maxUserNetworks bounds a user's networks in an environment, their default one included:
// each bridge takes a subnet from the daemon's address pools, which a few dozen exhaust for
// everyone on the host.
const maxUserNetworks = 8

// roomForNetwork takes user id's lock in env (their creates run one at a time) and returns
// its unlock if they may create another network, else a 409.
func (s *server) roomForNetwork(ctx context.Context, c docker.Client, env, id int64) (func(), error) {
	unlock, err := s.allowances.Lock(ctx, id, env)
	if err != nil {
		return nil, err
	}
	n, err := s.ownedNetworks(ctx, c)
	if err == nil && n >= maxUserNetworks {
		err = &statusError{code: http.StatusConflict, msg: fmt.Sprintf("you have %d networks here, the most a user may have: remove one first", n)}
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// ownedNetworks counts the request's user's networks.
func (s *server) ownedNetworks(ctx context.Context, c docker.Client) (int, error) {
	sc, err := s.scopeOf(ctx)
	if err != nil {
		return 0, err
	}
	res, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: sc.filters(nil)})
	if err != nil {
		return 0, fmt.Errorf("list networks: %w", err)
	}
	n := 0
	for _, it := range res.Items {
		if sc.owns(it.Labels) {
			n++
		}
	}
	return n, nil
}

// requestUser returns the request's user, and whether they are an admin.
func requestUser(r *http.Request) (auth.User, bool, error) {
	u, ok := userFrom(r.Context())
	if !ok {
		return auth.User{}, false, errors.New("no user in the request")
	}
	return u, isAdmin(u), nil
}

// userName returns a user's name for their new network or volume (the prefix and name),
// or the problem with it.
func userName(id int64, name string) (string, string) {
	if name == "" {
		return "", "name is required"
	}
	full := userPrefix(id) + name
	if !objectID.MatchString(full) {
		return "", "name is too long"
	}
	return full, ""
}

// userNetworkRefusal is why a user may not create the network req, or "". Users get
// bridge networks with Docker's own addressing: other drivers reach the host's network
// (host, macvlan, ipvlan) or run plugins, driver options change the bridge on the host,
// and a chosen subnet could shadow the host's own routes.
func userNetworkRefusal(req createNetworkRequest) string {
	switch {
	case req.Driver != "" && req.Driver != "bridge":
		return "users may only create bridge networks"
	case len(req.Options) > 0:
		return "users may not set network driver options"
	case req.Subnet != "" || req.Gateway != "" || req.IPRange != "":
		return "users may not choose network addresses"
	case req.IPv6:
		return "users may not enable IPv6"
	case req.Attachable:
		return "users may not create attachable networks"
	}
	return ""
}

// userVolumeRefusal is why a user may not create the volume req, or "". Only the local
// driver without options: "o=bind" with "device=" mounts a host path, other options a
// filesystem or a remote share.
func userVolumeRefusal(req createVolumeRequest) string {
	switch {
	case req.Driver != "" && req.Driver != "local":
		return "users may only create local volumes"
	case len(req.Options) > 0:
		return "users may not set volume driver options"
	}
	return ""
}

// errNameTaken answers a user's create whose name belongs to someone else (a deleted user's
// leftover, or an admin's object of that name).
func errNameTaken(what string) error {
	return &statusError{code: http.StatusConflict, msg: "a " + what + " with this name already exists"}
}

// ensureDefaultNetwork creates the default network of the user with owner key key in the
// environment c reaches, unless it is there. One of that name the user doesn't own is
// left alone and reported.
func ensureDefaultNetwork(ctx context.Context, c docker.Client, id int64, key string) error {
	name := defaultNetwork(id)
	owned := func() (bool, error) {
		res, err := c.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if err != nil {
			return false, err
		}
		if res.Network.Labels[ownerLabel] != key {
			return false, fmt.Errorf("network %s exists and isn't the user's", name)
		}
		return true, nil
	}
	ok, err := owned()
	if ok || err != nil && !docker.NotFound(err) {
		return err
	}
	_, err = c.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge", Options: userNetworkOptions(name), Labels: map[string]string{ownerLabel: key},
	})
	if docker.Conflict(err) {
		// Created meanwhile, by another grant or someone else.
		_, err = owned()
	}
	if err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// ensureDefaultNetworks creates user a's default network in the environments they were just
// given (added). Best effort: an offline environment or a leftover network of that name is
// logged, and the access stays.
func (s *server) ensureDefaultNetworks(ctx context.Context, a auth.Account, added []int64) {
	if a.Role == auth.RoleAdmin || len(added) == 0 {
		return
	}
	key, err := s.auth.OwnerKey(ctx, a.ID)
	if err != nil {
		s.log.Warn("default network: owner key", "username", a.Username, "err", err)
		return
	}
	for _, envID := range added {
		err := func() error {
			env, err := s.envs.Get(ctx, envID)
			if err != nil {
				return err
			}
			c, err := s.envs.Client(env)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
			defer cancel()
			return ensureDefaultNetwork(ctx, c, a.ID, key)
		}()
		if err != nil {
			s.log.Warn("default network", "username", a.Username, "env", envID, "err", err)
		}
	}
}

// addedIDs are the ids in after that aren't in before.
func addedIDs(before, after []int64) []int64 {
	var out []int64
	for _, id := range after {
		if !slices.Contains(before, id) {
			out = append(out, id)
		}
	}
	return out
}
