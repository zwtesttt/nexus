package host

import (
	"github.com/am6737/nexus/api"
	"github.com/am6737/nexus/transport/protocol/udp"
	"github.com/am6737/nexus/transport/protocol/udp/header"
	"github.com/flynn/noise"
	"github.com/sirupsen/logrus"
	"net"
	"sync"
	"sync/atomic"
)

type CachedPacket struct {
	messageType    header.MessageType
	messageSubType header.MessageSubType
	callback       packetCallback
	packet         []byte
}

type packetCallback func(t header.MessageType, st header.MessageSubType, h *HostInfo, p, nb, out []byte)

func NewHostMap(logger *logrus.Logger, vpnCIDR *net.IPNet, preferredRanges []*net.IPNet) *HostMap {
	h := map[api.VpnIp]*HostInfo{}
	i := map[uint32]*HostInfo{}
	r := map[uint32]*HostInfo{}
	relays := map[uint32]*HostInfo{}
	m := HostMap{
		Indexes:       i,
		Relays:        relays,
		RemoteIndexes: r,
		Hosts:         h,
		//preferredRanges: preferredRanges,
		vpnCIDR: vpnCIDR,
		logger:  logger,
	}
	return &m
}

type HostMap struct {
	sync.RWMutex  //Because we concurrently read and write to our maps
	Indexes       map[uint32]*HostInfo
	Relays        map[uint32]*HostInfo // Maps a Relay IDX to a Relay HostInfo object
	RemoteIndexes map[uint32]*HostInfo
	Hosts         map[api.VpnIp]*HostInfo
	logger        *logrus.Logger

	preferredRanges []*net.IPNet
	vpnCIDR         *net.IPNet
	metricsEnabled  bool
}

func (hm *HostMap) DeleteHost(vip api.VpnIp) {
	hm.Lock()
	defer hm.Unlock()
	delete(hm.Hosts, vip)
}

func (hm *HostMap) UpdateHost(vip api.VpnIp, udpAddr *udp.Addr) {
	hm.Lock()
	defer hm.Unlock()
	if hostInfo, ok := hm.Hosts[vip]; ok {
		hostInfo.Remote = &udp.Addr{
			IP:   udpAddr.IP,
			Port: uint16(udpAddr.Port),
		}
	} else {
		hm.Hosts[vip] = &HostInfo{
			Remote: &udp.Addr{
				IP:   udpAddr.IP,
				Port: uint16(udpAddr.Port),
			},
			Remotes: RemoteList{},
			VpnIp:   vip,
		}
	}
}

func (hm *HostMap) AddHost(vpnIP api.VpnIp, udpAddr *udp.Addr) {
	hm.Lock()
	defer hm.Unlock()
	hm.Hosts[vpnIP] = &HostInfo{
		Remote: &udp.Addr{
			IP:   udpAddr.IP,
			Port: udpAddr.Port,
		},
		Remotes: RemoteList{},
		VpnIp:   vpnIP,
	}
}

func (hm *HostMap) QueryVpnIp(vpnIp api.VpnIp) *HostInfo {
	//return hm.queryVpnIp(vpnIp, nil)
	return hm.queryVpnIp(vpnIp)
}

func (hm *HostMap) queryVpnIp(vpnIp api.VpnIp) *HostInfo {
	hm.RLock()
	if h, ok := hm.Hosts[vpnIp]; ok {
		hm.RUnlock()
		// Do not attempt promotion if you are a lighthouse
		//if promoteIfce != nil && !promoteIfce.lightHouse.amLighthouse {
		//	h.TryPromoteBest(hm.preferredRanges, promoteIfce)
		//}
		return h

	}

	hm.RUnlock()
	return nil
}

// GetRemoteAddrList 返回指定 VPN IP 的主机的远程地址列表
func (hm *HostMap) GetRemoteAddrList(vpnIP api.VpnIp) []*udp.Addr {
	hm.RLock()
	defer hm.RUnlock()

	if hostInfo, ok := hm.Hosts[vpnIP]; ok {
		return hostInfo.GetRemoteAddrList()
	}

	return nil
}

// GetRemoteAddrList 返回主机的远程地址列表
func (h *HostInfo) GetRemoteAddrList() []*udp.Addr {
	h.Remotes.RLock()
	defer h.Remotes.RUnlock()

	// 检查主机是否有单个远程地址
	if h.Remote != nil {
		return []*udp.Addr{h.Remote}
	}

	// 返回 RemoteList 中的地址列表
	return h.Remotes.addrs
}

type HostInfo struct {
	Remote  *udp.Addr
	Remotes RemoteList
	//ConnectionState *ConnectionState
	RemoteIndexId uint32
	LocalIndexId  uint32
	VpnIp         api.VpnIp
}

// RemoteList is a unifying concept for lighthouse servers and clients as well as hostinfos.
// It serves as a local cache of query replies, host update notifications, and locally learned addresses
type RemoteList struct {
	// Every interaction with internals requires a lock!
	sync.RWMutex

	// A deduplicated set of addresses. Any accessor should lock beforehand.
	addrs []*udp.Addr
}

type ConnectionState struct {
	//eKey           *CipherState
	//dKey           *CipherState
	H              *noise.HandshakeState
	initiator      bool
	messageCounter atomic.Uint64
	//window         *Bits
	writeLock sync.Mutex
}
