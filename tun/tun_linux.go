//go:build !android && !e2e_testing
// +build !android,!e2e_testing

package tun

import (
	"fmt"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const DefaultMTU = 1300

type ifReq struct {
	Name  [16]byte
	Flags uint16
	pad   [8]byte
}

type ifreqAddr struct {
	Name [16]byte
	Addr unix.RawSockaddrInet4
	pad  [8]byte
}

type ifreqMTU struct {
	Name [16]byte
	MTU  int32
	pad  [8]byte
}

type ifreqQLEN struct {
	Name  [16]byte
	Value int32
	pad   [8]byte
}

// tun 实现了 Device 接口
type tun struct {
	fd int
	io.ReadWriteCloser
	device     string
	addr       [4]byte
	mask       [4]byte
	defaultMTU int
	ifra       ifreqAddr
	cidr       *net.IPNet

	Routes []Route

	MaxMTU     int
	DefaultMTU int
	TXQueueLen int
}

func newTun(deviceName string, cidr *net.IPNet, defaultMTU int, txQueueLen int, multiqueue bool) (Device, error) {
	fd, err := unix.Open("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	var req ifReq
	req.Flags = uint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if multiqueue {
		req.Flags |= unix.IFF_MULTI_QUEUE
	}
	copy(req.Name[:], deviceName)
	if err = ioctl(uintptr(fd), uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&req))); err != nil {
		return nil, err
	}
	name := strings.Trim(string(req.Name[:]), "\x00")

	file := os.NewFile(uintptr(fd), "/dev/net/tun")

	if defaultMTU == 0 {
		defaultMTU = DefaultMTU
	}

	maxMTU := defaultMTU

	t := &tun{
		ReadWriteCloser: file,
		fd:              int(file.Fd()),
		device:          name,
		cidr:            cidr,
		MaxMTU:          maxMTU,
		DefaultMTU:      defaultMTU,
		TXQueueLen:      txQueueLen,
	}
	return t, nil
}

func ioctl(a1, a2, a3 uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, a1, a2, a3)
	if errno != 0 {
		return errno
	}
	return nil
}

//func (t *tun) Read(p []byte) (n int, err error) {
//	var nn int
//	max := len(p)
//
//	for {
//		n, err := unix.Write(t.fd, p[nn:max])
//		if n > 0 {
//			nn += n
//		}
//		if nn == len(p) {
//			return nn, err
//		}
//
//		if err != nil {
//			return nn, err
//		}
//
//		if n == 0 {
//			return nn, io.ErrUnexpectedEOF
//		}
//	}
//}

func (t *tun) Write(b []byte) (int, error) {
	var nn int
	max := len(b)

	for {
		n, err := unix.Write(t.fd, b[nn:max])
		if n > 0 {
			nn += n
		}
		if nn == len(b) {
			return nn, err
		}

		if err != nil {
			return nn, err
		}

		if n == 0 {
			return nn, io.ErrUnexpectedEOF
		}
	}
}

func (t *tun) Close() error {
	if t.ReadWriteCloser != nil {
		t.ReadWriteCloser.Close()
	}

	return nil
}

func (t *tun) MTU() int {
	return t.defaultMTU
}

func (t *tun) Cidr() *net.IPNet {
	return t.cidr
}

func (t *tun) Name() string {
	return t.device
}

func (t *tun) deviceBytes() (o [16]byte) {
	for i, c := range t.device {
		o[i] = byte(c)
	}
	return
}

func (t *tun) Up() error {
	devName := t.deviceBytes()

	var addr, mask [4]byte

	copy(addr[:], t.cidr.IP.To4())
	copy(mask[:], t.cidr.Mask)

	s, err := unix.Socket(
		unix.AF_INET,
		unix.SOCK_DGRAM,
		unix.IPPROTO_IP,
	)
	if err != nil {
		return err
	}
	fd := uintptr(s)

	ifra := ifreqAddr{
		Name: devName,
		Addr: unix.RawSockaddrInet4{
			Family: unix.AF_INET,
			Addr:   addr,
		},
	}

	// Set the device ip address
	if err = ioctl(fd, unix.SIOCSIFADDR, uintptr(unsafe.Pointer(&ifra))); err != nil {
		return fmt.Errorf("failed to set tun address: %s", err)
	}

	// Set the device network
	ifra.Addr.Addr = mask
	if err = ioctl(fd, unix.SIOCSIFNETMASK, uintptr(unsafe.Pointer(&ifra))); err != nil {
		return fmt.Errorf("failed to set tun netmask: %s", err)
	}

	// Set the device name
	ifrf := ifReq{Name: devName}
	if err = ioctl(fd, unix.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifrf))); err != nil {
		return fmt.Errorf("failed to set tun device name: %s", err)
	}

	// Set the MTU on the device
	ifm := ifreqMTU{Name: devName, MTU: int32(t.MaxMTU)}
	if err = ioctl(fd, unix.SIOCSIFMTU, uintptr(unsafe.Pointer(&ifm))); err != nil {
		// This is currently a non fatal condition because the route table must have the MTU set appropriately as well
		//t.l.WithError(err).Error("Failed to set tun mtu")
		return err
	}

	// Set the transmit queue length
	ifrq := ifreqQLEN{Name: devName, Value: int32(t.TXQueueLen)}
	if err = ioctl(fd, unix.SIOCSIFTXQLEN, uintptr(unsafe.Pointer(&ifrq))); err != nil {
		// If we can't set the queue length nebula will still work but it may lead to packet loss
		//t.l.WithError(err).Error("Failed to set tun tx queue length")
		return err
	}

	// Bring up the interface
	ifrf.Flags = ifrf.Flags | unix.IFF_UP
	if err = ioctl(fd, unix.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifrf))); err != nil {
		return fmt.Errorf("failed to bring the tun device up: %s", err)
	}

	// Set the routes
	link, err := netlink.LinkByName(t.device)
	if err != nil {
		return fmt.Errorf("failed to get tun device link: %s", err)
	}

	// Default route
	dr := &net.IPNet{IP: t.cidr.IP.Mask(t.cidr.Mask), Mask: t.cidr.Mask}
	nr := netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dr,
		MTU:       t.DefaultMTU,
		AdvMSS:    t.advMSS(Route{}),
		Scope:     unix.RT_SCOPE_LINK,
		Src:       t.cidr.IP,
		Protocol:  unix.RTPROT_KERNEL,
		Table:     unix.RT_TABLE_MAIN,
		Type:      unix.RTN_UNICAST,
	}
	err = netlink.RouteReplace(&nr)
	if err != nil {
		return fmt.Errorf("failed to set mtu %v on the default route %v; %v", t.DefaultMTU, dr, err)
	}

	// Path routes
	for _, r := range t.Routes {
		if !r.Install {
			continue
		}

		nr := netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       r.Cidr,
			MTU:       r.MTU,
			AdvMSS:    t.advMSS(r),
			Scope:     unix.RT_SCOPE_LINK,
		}

		if r.Metric > 0 {
			nr.Priority = r.Metric
		}

		err = netlink.RouteAdd(&nr)
		if err != nil {
			return fmt.Errorf("failed to set mtu %v on route %v; %v", r.MTU, r.Cidr, err)
		}
	}

	// Run the interface
	ifrf.Flags = ifrf.Flags | unix.IFF_UP | unix.IFF_RUNNING
	if err = ioctl(fd, unix.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifrf))); err != nil {
		return fmt.Errorf("failed to run tun device: %s", err)
	}

	return nil
}

func (t *tun) advMSS(r Route) int {
	mtu := r.MTU
	if r.MTU == 0 {
		mtu = t.DefaultMTU
	}

	// We only need to set advmss if the route MTU does not match the device MTU
	if mtu != t.MaxMTU {
		return mtu - 40
	}
	return 0
}

func (t *tun) Down() error {
	if t.ReadWriteCloser != nil {
		return t.ReadWriteCloser.Close()
	}

	return nil
}
