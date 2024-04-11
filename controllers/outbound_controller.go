package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/am6737/nexus/api"
	"github.com/am6737/nexus/api/interfaces"
	"github.com/am6737/nexus/config"
	"github.com/am6737/nexus/host"
	"github.com/am6737/nexus/transport/packet"
	"github.com/am6737/nexus/transport/protocol/udp"
	"github.com/am6737/nexus/transport/protocol/udp/header"
	"github.com/am6737/nexus/utils"
	"github.com/sirupsen/logrus"
	"net"
	"runtime"
)

var _ interfaces.OutboundController = &OutboundController{}

// OutboundController 出站控制器 必须实现 interfaces.OutboundController 接口
type OutboundController struct {
	outside     udp.Conn
	hosts       *host.HostMap
	lighthouses []*host.HostInfo
	localVpnIP  api.VpnIp
	logger      *logrus.Logger
	cfg         *config.Config
	lighthouse  interfaces.LighthouseController
}

func (oc *OutboundController) WriteToAddr(p []byte, addr net.Addr) error {
	parseIPAndPort := func(addr net.Addr) (net.IP, uint16) {
		switch a := addr.(type) {
		case *net.TCPAddr:
			return a.IP.To4(), uint16(a.Port)
		case *net.UDPAddr:
			return a.IP.To4(), uint16(a.Port)
		default:
			// 处理未知类型的 net.Addr
			return nil, 0
		}
	}
	ip, port := parseIPAndPort(addr)
	oc.logger.WithField("addr", addr).Info("出站流量 SendToRemote")
	return oc.outside.WriteTo(p, &udp.Addr{
		IP:   ip,
		Port: port,
	})
}

func (oc *OutboundController) WriteToVIP(p []byte, vip api.VpnIp) error {
	messagePacket, err := header.BuildMessagePacket(9527, 111)
	if err != nil {
		return err
	}

	// 创建新的数据包，将头部和数据包拼接
	p = append(messagePacket, p...)

	host := oc.hosts.QueryVpnIp(vip)
	if host == nil {
		for _, lighthouse := range oc.lighthouses {
			if lighthouse != nil {
				oc.logger.WithField("目标地址", vip).
					WithField("灯塔地址", lighthouse.Remote).
					Info("出站流量转发到灯塔 OutboundController => Lighthouse")
				return oc.outside.WriteTo(p, lighthouse.Remote)
			}
		}
		return fmt.Errorf("host %s not found", vip)
	}

	oc.logger.WithField("目标地址", vip).
		WithField("目标远程地址", host.Remote).
		WithField("数据包", p).
		Info("出站流量")
	return oc.outside.WriteTo(p, host.Remote)
}

func (oc *OutboundController) SendToRemote(out []byte, addr *udp.Addr) error {
	oc.logger.WithField("addr", addr).Info("出站流量 SendToRemote")
	return oc.outside.WriteTo(out, addr)
}

func (oc *OutboundController) Send(out []byte, vip api.VpnIp) error {
	messagePacket, err := header.BuildMessagePacket(9527, 111)
	if err != nil {
		return err
	}

	// 创建新的数据包，将头部和数据包拼接
	out = append(messagePacket, out...)

	host := oc.hosts.QueryVpnIp(vip)
	if host == nil {
		for _, lighthouse := range oc.lighthouses {
			if lighthouse != nil {
				oc.logger.WithField("目标地址", vip).
					WithField("灯塔地址", lighthouse.Remote).
					Info("出站流量转发到灯塔 OutboundController => Lighthouse")
				return oc.outside.WriteTo(out, lighthouse.Remote)
			}
		}
		return fmt.Errorf("host %s not found", vip)
	}

	oc.logger.WithField("目标地址", vip).
		WithField("目标远程地址", host.Remote).
		WithField("数据包", out).
		Info("出站流量")
	return oc.outside.WriteTo(out, host.Remote)
}

func (oc *OutboundController) Start(ctx context.Context) error {
	//// 解析监听主机地址
	//listenHost, err := resolveListenHost(oc.cfg.Listen.Host)
	//if err != nil {
	//	return err
	//}
	//
	//// 设置 UDP 服务器
	//udpServer, err := udp.NewListener(oc.logger, listenHost.IP, oc.cfg.Listen.Port, oc.cfg.Listen.Routines > 1, oc.cfg.Listen.Batch)
	////udpServer, err := udp.NewGenericListener(oc.logger, listenHost.IP, oc.cfg.Listen.Port, oc.cfg.Listen.Routines > 1, oc.cfg.Listen.Batch)
	//if err != nil {
	//	return err
	//}
	//udpServer.ReloadConfig(oc.cfg)
	//oc.outside = udpServer

	// 如果端口是动态的，则获取端口
	if oc.cfg.Listen.Port == 0 {
		uPort, err := oc.outside.LocalAddr()
		if err != nil {
			return err
		}
		oc.cfg.Listen.Port = int(uPort.Port)
	}

	// 配置静态主机映射
	oc.configureStaticHostMap()

	// 获取灯塔信息
	oc.lighthouses = oc.getLighthouses()

	addr, err := oc.outside.LocalAddr()
	if err != nil {
		return err
	}
	oc.logger.WithField("udpAddr", addr).Info("Starting outbound controller")
	return nil
}

func resolveListenHost(rawListenHost string) (*net.IPAddr, error) {
	if rawListenHost == "[::]" {
		// Old guidance was to provide the literal `[::]` in `listen.host` but that won't resolve.
		return &net.IPAddr{IP: net.IPv6zero}, nil
	}
	return net.ResolveIPAddr("ip", rawListenHost)
}

func (oc *OutboundController) configureStaticHostMap() {
	for k, v := range oc.cfg.StaticHostMap {
		ip := net.ParseIP(k)
		if ip == nil {
			oc.logger.WithField("ip", k).Error("Invalid IP address")
			continue
		}
		udpAddr, err := net.ResolveUDPAddr("udp", v[0])
		if err != nil {
			oc.logger.WithError(err).WithField("ip", k).Error("Error resolving UDP address")
			continue
		}
		vpnIp := api.Ip2VpnIp(ip)
		oc.hosts.AddHost(vpnIp, &udp.Addr{
			IP:   udpAddr.IP,
			Port: uint16(udpAddr.Port),
		})
	}
}

func (oc *OutboundController) getLighthouses() []*host.HostInfo {
	var lighthouses []*host.HostInfo
	for _, ip := range oc.cfg.Lighthouse.Hosts {
		vpnIp, err := api.ParseVpnIp(ip)
		if err != nil {
			oc.logger.WithError(err).WithField("lighthouse", ip).Error("解析VPN地址失败")
			continue
		}
		host := oc.hosts.QueryVpnIp(vpnIp)
		if host != nil {
			lighthouses = append(lighthouses, host)
		} else {
			oc.logger.WithField("lighthouse", vpnIp).Error("灯塔未配置静态地址映射")
		}
	}
	return lighthouses
}

func (oc *OutboundController) handlePacket(addr *udp.Addr, p []byte, h *header.Header, internalWriter interfaces.InsideWriter) {
	pk := &packet.Packet{}

	if err := h.Decode(p); err != nil {
		oc.logger.WithError(err).Error("解析数据包头出错")
		return
	}

	//fmt.Println("p[header.Len:] => ", p[header.Len:])

	// 解析数据包
	// 将incoming参数设置为true
	if err := utils.ParsePacket(p[header.Len:], true, pk); err != nil {
		oc.logger.WithError(err).Error("解析数据包出错")
		return
	}

	oc.logger.WithField("远程地址", addr).
		WithField("源地址", pk.LocalIP).
		WithField("目标地址", pk.RemoteIP).
		WithField("数据包", pk).
		WithField("原始数据", p).
		Info("入站流量")

	switch h.MessageType {
	case header.Handshake:
		oc.logger.
			WithField("握手数据包", pk).
			WithField("远程地址", addr).
			Info("收到握手数据包")
		oc.handleHandshake(addr, pk, h, p)
	case header.Message:
		out := p
		p = p[header.Len:]

		if pk.RemoteIP == oc.localVpnIP {
			fmt.Println("u1")
			replaceAddresses(p, pk.LocalIP, pk.RemoteIP)
			oc.handleLocalVpnAddress(p, pk, internalWriter)
			return
		}

		if oc.localVpnIP == pk.LocalIP {
			fmt.Println("u2")
			if pk.Protocol != packet.ProtoICMP {
				oc.handleLocalVpnAddress(p, pk, internalWriter)
			}
			if err := oc.outside.WriteTo(out, addr); err != nil {
				oc.logger.WithError(err).WithField("addr", addr).Error("数据转发到远程")
			}
		}
		// 更新 remotes 映射表
		oc.updateRemotes(pk, addr)
	case header.LightHouse:
		// 处理目标地址是灯塔的情况
		oc.handleLighthouses(addr, pk, h, p)
	default:

	}
}

func (oc *OutboundController) handleHandshake(addr *udp.Addr, pk *packet.Packet, h *header.Header, p []byte) {
	oc.hosts.AddHost(pk.RemoteIP, addr)
	fmt.Println("oc.hosts => ", oc.hosts.Hosts)
	fmt.Println("h.MessageSubtype => ", h.MessageSubtype)
	switch h.MessageSubtype {
	case header.HostSync:
		fmt.Println("oc.hosts.Hosts => ", oc.hosts.Hosts)
		hp, _ := json.Marshal(oc.hosts.Hosts)
		replyPacket, err := oc.buildHandshakeHostSyncReplyPacket(pk.RemoteIP, hp)
		if err != nil {
			oc.logger.WithError(err).Error("构建握手数据包出错")
			return
		}
		oc.logger.
			WithField("RemoteIP", pk.RemoteIP).
			WithField("addr", addr).
			WithField("p", replyPacket).
			WithField("hp", hp).
			Info("发送主机同步回复数据包")
		if err := oc.outside.WriteTo(replyPacket, addr); err != nil {
			oc.logger.WithError(err).Error("数据转发到远程")
		}
	case header.HostSyncReply:
		oc.logger.
			WithField("pk", pk).
			WithField("p", p).
			Info("收到灯塔同步回复数据包")
		p = p[header.Len+20:]
		var hs map[api.VpnIp]*host.HostInfo
		if err := json.Unmarshal(p, &hs); err != nil {
			oc.logger.WithError(err).Error("解析数据包出错")
			return
		}
		for i, i2 := range hs {
			fmt.Printf("节点地址: %s info: %v", i, i2)
		}
	}
}

func (oc *OutboundController) buildHandshakeHostSyncReplyPacket(vip api.VpnIp, data []byte) ([]byte, error) {
	handshakePacket, err := header.BuildHandshakePacket(0, header.HostSyncReply, 0)
	if err != nil {
		return nil, err
	}
	pv4Packet, err := packet.BuildIPv4Packet(oc.localVpnIP.ToIP(), vip.ToIP(), packet.ProtoUDP, false)
	if err != nil {
		return nil, err
	}

	fmt.Println("pv4Packet => ", len(pv4Packet))

	var buf bytes.Buffer
	buf.Write(handshakePacket)
	buf.Write(pv4Packet)
	buf.Write(data)
	return buf.Bytes(), nil
}

// 更新 remotes 映射表
func (oc *OutboundController) updateRemotes(pk *packet.Packet, addr *udp.Addr) {
	udpAddr := &net.UDPAddr{
		IP:   addr.IP,
		Port: int(addr.Port),
	}
	oc.hosts.UpdateHost(pk.RemoteIP, udpAddr)
}

// 处理目标地址是本地VPN地址的情况
func (oc *OutboundController) handleLocalVpnAddress(p []byte, pk *packet.Packet, internalWriter interfaces.InsideWriter) {
	if _, err := internalWriter.Write(p); err != nil {
		oc.logger.WithError(err).Error("写入数据出错")
	}
}

// 处理目标地址是灯塔的情况
func (oc *OutboundController) handleLighthouses(addr *udp.Addr, pk *packet.Packet, h *header.Header, p []byte) {
	oc.lighthouse.HandleRequest(addr, pk.LocalIP, h, p)
	//for _, lighthouse := range oc.lighthouses {
	//	if lighthouse != nil {
	//		oc.logger.WithField("目标地址", addr).
	//			WithField("灯塔地址", lighthouse.Remote).
	//			Info("出站流量转发到灯塔")
	//
	//		// 如果本地没有远程连接，将数据包转发到灯塔
	//		if err := oc.outside.WriteTo(p, lighthouse.Remote); err != nil {
	//			oc.logger.WithError(err).Error("数据转发到灯塔出错")
	//		}
	//	}
	//}
}

// Listen 监听出站连接，并根据目标地址将数据包转发到相应的目标
func (oc *OutboundController) Listen(internalWriter interfaces.InsideWriter) {
	runtime.LockOSThread()
	oc.outside.ListenOut(func(addr *udp.Addr, out []byte, p []byte, h *header.Header) {
		oc.handlePacket(addr, p, h, internalWriter)
	})
}

func (oc *OutboundController) Close() error {
	return oc.outside.Close()
}

func replaceAddresses(out []byte, localIP api.VpnIp, remoteIP api.VpnIp) {
	copy(out[12:16], parseIP(localIP.String()))  // 将本地IP地址替换到目标IP地址的位置
	copy(out[16:20], parseIP(remoteIP.String())) // 将目标IP地址替换到源IP地址的位置
}

func parseIP(ipString string) []byte {
	// 解析 IPv4 地址字符串为 net.IP 类型
	ip := net.ParseIP(ipString)
	if ip == nil {
		fmt.Println("Invalid IP address:", ipString)
		return nil
	}
	// 将 net.IP 类型转换为 []byte 切片
	ipBytes := ip.To4()
	if ipBytes == nil {
		fmt.Println("Invalid IPv4 address:", ipString)
		return nil
	}
	return ipBytes
}
