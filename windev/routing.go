//go:build windows

package windev

// Windows interface configuration (address / routes / DNS) shared by every
// Windows front-end: the standalone veil-client and the tunnel service that the
// Legacy GUI fork drives. It reproduces Windows's routing
// behaviour via the winipcfg LUID API and fixes the full-tunnel routing loop by
// pinning a /32 host route to the server endpoint over the physical gateway.

import (
	"log"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/sys/windows"
	"github.com/veil-proto/veil-windows/winipcfg"

	"github.com/veil-proto/veil/config"
	"github.com/veil-proto/veil/engine"
)

// ConfigureRouting sets the tunnel interface address, the endpoint-exception
// route, the AllowedIPs routes and DNS for the given tunnel LUID. It returns a
// cleanup function that reverses the changes; call it on shutdown.
func ConfigureRouting(tunLUID winipcfg.LUID, cfg *config.Config) (func(), error) {
	if addr, err := netip.ParsePrefix(cfg.Interface.Address); err == nil {
		if err := tunLUID.SetIPAddresses([]netip.Prefix{addr}); err != nil {
			return nil, err
		}
	}

	if ipif, err := tunLUID.IPInterface(windows.AF_INET); err == nil {
		ipif.NLMTU = uint32(engine.DefaultTunMTU)
		if err := ipif.Set(); err != nil {
			log.Printf("Warning: Set IPInterface MTU: %v", err)
		} else {
			log.Printf("Set IP interface MTU to %d", engine.DefaultTunMTU)
		}
	}

	var exceptionLUID winipcfg.LUID
	var exceptionHop netip.Addr
	var exceptionPrefix netip.Prefix

	if serverIP := resolveEndpoint(cfg); serverIP.IsValid() {
		if pluid, hop, found := defaultRoute(tunLUID); found {
			exceptionPrefix = netip.PrefixFrom(serverIP, serverIP.BitLen())
			if err := pluid.AddRoute(exceptionPrefix, hop, 0); err != nil {
				log.Printf("Warning: endpoint exception route: %v", err)
			} else {
				exceptionLUID, exceptionHop = pluid, hop
				log.Printf("Endpoint exception: %s via %s (physical)", exceptionPrefix, hop)
			}
		} else {
			log.Printf("Warning: no physical default route found; routing loop possible")
		}
	}

	addTunnelRoutes(tunLUID, cfg)

	if cfg.Interface.DNS != "" {
		var servers []netip.Addr
		for _, s := range strings.Split(cfg.Interface.DNS, ",") {
			if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
				servers = append(servers, a)
			}
		}
		if len(servers) > 0 {
			if err := tunLUID.SetDNS(windows.AF_INET, servers, nil); err != nil {
				log.Printf("Warning: SetDNS: %v", err)
			}
		}
	}

	cleanup := func() {
		tunLUID.FlushRoutes(windows.AF_INET)
		tunLUID.FlushDNS(windows.AF_INET)
		if exceptionLUID != 0 && exceptionPrefix.IsValid() {
			exceptionLUID.DeleteRoute(exceptionPrefix, exceptionHop)
		}
	}
	return cleanup, nil
}

// resolveEndpoint returns the IPv4 of the first peer endpoint.
func resolveEndpoint(cfg *config.Config) netip.Addr {
	for _, p := range cfg.Peers {
		if p.Endpoint == "" {
			continue
		}
		host, _, err := net.SplitHostPort(p.Endpoint)
		if err != nil {
			host = p.Endpoint
		}
		if ips, err := net.LookupIP(host); err == nil {
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					a, _ := netip.AddrFromSlice(v4)
					return a
				}
			}
		}
	}
	return netip.Addr{}
}

// defaultRoute finds the lowest-metric IPv4 default route not on the tunnel
// interface, returning its interface LUID and next hop.
func defaultRoute(exclude winipcfg.LUID) (winipcfg.LUID, netip.Addr, bool) {
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		log.Printf("GetIPForwardTable2: %v", err)
		return 0, netip.Addr{}, false
	}
	var best winipcfg.MibIPforwardRow2
	found := false
	for i := range rows {
		r := rows[i]
		p := r.DestinationPrefix.Prefix()
		if !p.Addr().Is4() || p.Bits() != 0 || r.InterfaceLUID == exclude {
			continue
		}
		if !found || r.Metric < best.Metric {
			best, found = r, true
		}
	}
	if !found {
		return 0, netip.Addr{}, false
	}
	return best.InterfaceLUID, best.NextHop.Addr(), true
}

// addTunnelRoutes installs peer AllowedIPs on the tunnel; 0.0.0.0/0 becomes a
// 0.0.0.0/1 + 128.0.0.0/1 split so it overrides the physical default by
// longest-prefix match without deleting it.
func addTunnelRoutes(luid winipcfg.LUID, cfg *config.Config) {
	onlink := netip.IPv4Unspecified()
	add := func(pfx netip.Prefix) {
		if err := luid.AddRoute(pfx, onlink, 0); err != nil {
			log.Printf("Warning: add route %s: %v", pfx, err)
		}
	}
	for _, peer := range cfg.Peers {
		for _, a := range peer.AllowedIPs {
			pfx, err := netip.ParsePrefix(strings.TrimSpace(a))
			if err != nil {
				continue
			}
			if pfx.Bits() == 0 && pfx.Addr().Is4() {
				add(netip.PrefixFrom(netip.IPv4Unspecified(), 1))
				add(netip.PrefixFrom(netip.AddrFrom4([4]byte{128, 0, 0, 0}), 1))
				continue
			}
			add(pfx)
		}
	}
}
