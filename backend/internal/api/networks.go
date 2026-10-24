package api

import (
	"context"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

type networkResponse struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	CreatedAt  *time.Time        `json:"created_at"`
	Internal   bool              `json:"internal"`
	Attachable bool              `json:"attachable"`
	Ingress    bool              `json:"ingress"`
	IPAM       []ipamResponse    `json:"ipam"`
	Labels     map[string]string `json:"labels"`
}

type ipamResponse struct {
	Subnet  string `json:"subnet"`
	IPRange string `json:"ip_range"`
	Gateway string `json:"gateway"`
}

type networkDetailResponse struct {
	networkResponse
	Options    map[string]string          `json:"options"`
	Containers []networkContainerResponse `json:"containers"`
}

type networkContainerResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4_address"`
	IPv6Address string `json:"ipv6_address"`
	MacAddress  string `json:"mac_address"`
}

func (s *server) listNetworks(w http.ResponseWriter, r *http.Request) {
	s.dockerCall(w, r, "list networks", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.NetworkList(ctx, client.NetworkListOptions{})
		if err != nil {
			return nil, err
		}
		resp := make([]networkResponse, len(res.Items))
		for i, n := range res.Items {
			resp[i] = toNetwork(n.Network)
		}
		sortBy(resp, func(n networkResponse) string { return n.Name })
		return resp, nil
	})
}

func (s *server) getNetwork(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "network")
	if !ok {
		return
	}
	s.dockerCall(w, r, "network", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
		if err != nil {
			return nil, err
		}
		n := res.Network
		resp := networkDetailResponse{networkResponse: toNetwork(n.Network), Options: nonNilMap(n.Options), Containers: []networkContainerResponse{}}
		for _, id := range sortedKeys(n.Containers) {
			ep := n.Containers[id]
			nc := networkContainerResponse{ID: id, Name: ep.Name, MacAddress: ep.MacAddress.String()}
			if ep.IPv4Address.IsValid() {
				nc.IPv4Address = ep.IPv4Address.String()
			}
			if ep.IPv6Address.IsValid() {
				nc.IPv6Address = ep.IPv6Address.String()
			}
			resp.Containers = append(resp.Containers, nc)
		}
		return resp, nil
	})
}

func toNetwork(n network.Network) networkResponse {
	r := networkResponse{
		ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal,
		Attachable: n.Attachable, Ingress: n.Ingress, IPAM: []ipamResponse{}, Labels: nonNilMap(n.Labels),
	}
	if !n.Created.IsZero() {
		t := n.Created.UTC()
		r.CreatedAt = &t
	}
	for _, c := range n.IPAM.Config {
		ir := ipamResponse{}
		if c.Subnet.IsValid() {
			ir.Subnet = c.Subnet.String()
		}
		if c.IPRange.IsValid() {
			ir.IPRange = c.IPRange.String()
		}
		if c.Gateway.IsValid() {
			ir.Gateway = c.Gateway.String()
		}
		r.IPAM = append(r.IPAM, ir)
	}
	return r
}
