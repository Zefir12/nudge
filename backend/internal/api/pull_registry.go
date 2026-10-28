package api

import (
	"fmt"
	"net/http"
	"strings"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

// DefaultUserRegistries are the registries users' pulls may reach when the server
// names none: Docker Hub and the big public registries. A user's pull names its
// registry in the image reference, and without a limit the daemon would connect to any
// host it can reach (10.0.0.5:5000/x), a probe of the internal network.
var DefaultUserRegistries = []string{
	registries.DockerHub,
	"ghcr.io",
	"quay.io",
	"gcr.io",
	"registry.k8s.io",
	"mcr.microsoft.com",
	"public.ecr.aws",
	"registry.gitlab.com",
	"lscr.io",
}

// registrySet is the registries users' pulls may reach.
type registrySet struct {
	any   bool
	hosts map[string]bool
}

// newRegistrySet returns the set of hosts ("*" for any); nil is DefaultUserRegistries.
func newRegistrySet(hosts []string) registrySet {
	if hosts == nil {
		hosts = DefaultUserRegistries
	}
	set := registrySet{hosts: map[string]bool{}}
	for _, h := range hosts {
		if h == "*" {
			set.any = true
			continue
		}
		set.hosts[canonicalRegistry(h)] = true
	}
	return set
}

// check refuses ref if the daemon would pull it from a registry outside the set.
func (rs registrySet) check(ref string) error {
	if rs.any {
		return nil
	}
	host := pullRegistry(ref)
	if rs.hosts[host] {
		return nil
	}
	return &statusError{code: http.StatusForbidden, msg: fmt.Sprintf("pulls from %s are not allowed for users: use an image from an allowed registry", host)}
}

// pullRegistry returns the registry the daemon pulls ref from, by Docker's own rule: the
// first path component is a registry if it has a dot or a port, is localhost or has an
// upper-case letter; otherwise the image is on Docker Hub. registries.HostOf ignores the
// upper-case case, which would let "Intranet/x" reach a host named intranet.
func pullRegistry(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	first, _, ok := strings.Cut(name, "/")
	if !ok || (!strings.ContainsAny(first, ".:") && first != "localhost" && strings.ToLower(first) == first) {
		return registries.DockerHub
	}
	return canonicalRegistry(first)
}

// canonicalRegistry is host in lower case, with Docker Hub's aliases as docker.io.
func canonicalRegistry(host string) string {
	host = strings.ToLower(host)
	if h, err := registries.NormalizeAddress(host); err == nil {
		return h
	}
	return host
}
