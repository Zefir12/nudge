package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// networkTimeout bounds a network create or remove.
const networkTimeout = time.Minute

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

// createNetworkRequest is a new network. An empty driver is "bridge"; without a subnet
// Docker picks one from its address pools. Gateway and IP range need a subnet.
type createNetworkRequest struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Internal   bool              `json:"internal"`
	Attachable bool              `json:"attachable"`
	IPv6       bool              `json:"ipv6"`
	Subnet     string            `json:"subnet"`
	Gateway    string            `json:"gateway"`
	IPRange    string            `json:"ip_range"`
	Options    map[string]string `json:"options"`
	Labels     map[string]string `json:"labels"`
}

type createNetworkResponse struct {
	ID       string   `json:"id"`
	Warnings []string `json:"warnings"`
}

type pruneNetworksResponse struct {
	Deleted []string `json:"deleted"`
}

// createNetwork creates a network and answers 201 with its ID.
func (s *server) createNetwork(w http.ResponseWriter, r *http.Request) {
	var req createNetworkRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	if req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if msg := validCreate(req.Name, req.Driver, req.Options, req.Labels); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}
	ipam, msg := toIPAM(req)
	if msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}
	opts := client.NetworkCreateOptions{
		Driver: req.Driver, Internal: req.Internal, Attachable: req.Attachable, IPAM: ipam,
		Options: req.Options, Labels: req.Labels,
	}
	if req.IPv6 {
		opts.EnableIPv6 = &req.IPv6
	}
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "network create", "a network with this name already exists", networkTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.NetworkCreate(ctx, req.Name, opts)
		if err != nil {
			return nil, createError(err, "network")
		}
		return createNetworkResponse{ID: res.ID, Warnings: nonNil(res.Warning)}, nil
	})
	if ok {
		s.writeJSON(w, http.StatusCreated, resp)
	}
}

// toIPAM parses the request's addresses; it returns nil without a subnet, and the
// problem with them, if any.
func toIPAM(req createNetworkRequest) (*network.IPAM, string) {
	if req.Subnet == "" {
		if req.Gateway != "" || req.IPRange != "" {
			return nil, "gateway and ip_range need a subnet"
		}
		return nil, ""
	}
	subnet, err := netip.ParsePrefix(req.Subnet)
	if err != nil || subnet != subnet.Masked() {
		return nil, "subnet must be a network in CIDR notation, such as 172.30.0.0/16"
	}
	cfg := network.IPAMConfig{Subnet: subnet}
	if req.Gateway != "" {
		gw, err := netip.ParseAddr(req.Gateway)
		if err != nil || !subnet.Contains(gw) {
			return nil, "gateway must be an address in the subnet"
		}
		cfg.Gateway = gw
	}
	if req.IPRange != "" {
		ipRange, err := netip.ParsePrefix(req.IPRange)
		if err != nil || ipRange != ipRange.Masked() || ipRange.Bits() < subnet.Bits() || !subnet.Contains(ipRange.Addr()) {
			return nil, "ip_range must be a network in CIDR notation inside the subnet"
		}
		cfg.IPRange = ipRange
	}
	return &network.IPAM{Config: []network.IPAMConfig{cfg}}, ""
}

// removeNetwork removes a network. Docker refuses predefined networks (bridge, host,
// none) and networks with containers attached.
func (s *server) removeNetwork(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "network")
	if !ok {
		return
	}
	s.dockerAction(w, r, "network", "the network has containers attached or is one of Docker's predefined networks", networkTimeout, func(ctx context.Context, c docker.Client) error {
		_, err := c.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
		if docker.Forbidden(err) {
			return docker.ErrConflict
		}
		return err
	})
}

// pruneNetworks removes the custom networks no container uses.
func (s *server) pruneNetworks(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "network prune", "a network prune is already running", pruneTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.NetworkPrune(ctx, client.NetworkPruneOptions{})
		if err != nil {
			return nil, err
		}
		return pruneNetworksResponse{Deleted: nonNil(res.Report.NetworksDeleted)}, nil
	})
	if ok {
		s.writeJSON(w, http.StatusOK, resp)
	}
}
