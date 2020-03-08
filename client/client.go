package client

import (
	"errors"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
	"ssh-microservice/common"
	"sync"
)

type (
	Client struct {
		options       map[string]*common.ConnectOption
		tunnels       map[string]*[]common.TunnelOption
		runtime       map[string]*ssh.Client
		localListener *safeMapListener
		localConn     *safeMapConn
		remoteConn    *safeMapConn
	}
	safeMapListener struct {
		sync.RWMutex
		Map map[string]map[string]*net.Listener
	}
	safeMapConn struct {
		sync.RWMutex
		Map map[string]map[string]*net.Conn
	}
	Information struct {
		Identity  string                `json:"identity"`
		Host      string                `json:"host"`
		Port      uint32                `json:"port"`
		Username  string                `json:"username"`
		Connected string                `json:"connected"`
		Tunnels   []common.TunnelOption `json:"tunnels"`
	}
)

// Inject ssh client service
func InjectClient() *Client {
	sshClient := new(Client)
	sshClient.options = make(map[string]*common.ConnectOption)
	sshClient.tunnels = make(map[string]*[]common.TunnelOption)
	sshClient.runtime = make(map[string]*ssh.Client)
	sshClient.localListener = newSafeMapListener()
	sshClient.localConn = newSafeMapConn()
	sshClient.remoteConn = newSafeMapConn()
	configs, err := common.GetTemporary()
	if err != nil {
		log.Fatalln(err)
	}
	if configs.Connect != nil {
		sshClient.options = configs.Connect
	}
	for identity, option := range configs.Connect {
		err = sshClient.Put(identity, *option)
		if err != nil {
			log.Fatalln(err)
		}
	}
	if configs.Tunnel != nil {
		sshClient.tunnels = configs.Tunnel
	}
	for identity, options := range configs.Tunnel {
		err = sshClient.SetTunnels(identity, *options)
		if err != nil {
			log.Fatalln(err)
		}
	}
	return sshClient
}

func newSafeMapListener() *safeMapListener {
	listener := new(safeMapListener)
	listener.Map = make(map[string]map[string]*net.Listener)
	return listener
}

func (s *safeMapListener) Clear(identity string) {
	s.Map[identity] = make(map[string]*net.Listener)
}

func (s *safeMapListener) Get(identity string, addr string) *net.Listener {
	s.RLock()
	listener := s.Map[identity][addr]
	s.RUnlock()
	return listener
}

func (s *safeMapListener) Set(identity string, addr string, listener *net.Listener) {
	s.Lock()
	s.Map[identity][addr] = listener
	s.Unlock()
}

func newSafeMapConn() *safeMapConn {
	listener := new(safeMapConn)
	listener.Map = make(map[string]map[string]*net.Conn)
	return listener
}

func (s *safeMapConn) Clear(identity string) {
	s.Map[identity] = make(map[string]*net.Conn)
}

func (s *safeMapConn) Get(identity string, addr string) *net.Conn {
	s.RLock()
	conn := s.Map[identity][addr]
	s.RUnlock()
	return conn
}

func (s *safeMapConn) Set(identity string, addr string, conn *net.Conn) {
	s.Lock()
	s.Map[identity][addr] = conn
	s.Unlock()
}

// Get Client Options
func (c *Client) GetClientOptions() map[string]*common.ConnectOption {
	return c.options
}

// Generate AuthMethod
func (c *Client) authMethod(option common.ConnectOption) (auth []ssh.AuthMethod, err error) {
	if option.Key == nil {
		// Password AuthMethod
		auth = []ssh.AuthMethod{
			ssh.Password(option.Password),
		}
	} else {
		// PrivateKey AuthMethod
		var signer ssh.Signer
		if option.PassPhrase != nil {
			// With Passphrase
			if signer, err = ssh.ParsePrivateKeyWithPassphrase(
				option.Key,
				option.PassPhrase,
			); err != nil {
				return
			}
		} else {
			// Without Passphrase
			if signer, err = ssh.ParsePrivateKey(
				option.Key,
			); err != nil {
				return
			}
		}
		auth = []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		}
	}
	return
}

// Ssh client connection
func (c *Client) connect(option common.ConnectOption) (client *ssh.Client, err error) {
	auth, err := c.authMethod(option)
	if err != nil {
		return
	}
	config := ssh.ClientConfig{
		User:            option.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	addr := common.GetAddr(option.Host, uint(option.Port))
	client, err = ssh.Dial("tcp", addr, &config)
	return
}

// Test ssh client connection
func (c *Client) Testing(option common.ConnectOption) (sshClient *ssh.Client, err error) {
	return c.connect(option)
}

// Add or modify the ssh client
func (c *Client) Put(identity string, option common.ConnectOption) (err error) {
	err = c.Delete(identity)
	if err != nil {
		return
	}
	if c.tunnels[identity] != nil {
		c.closeTunnel(identity)
	}
	if c.runtime[identity] != nil {
		c.runtime[identity].Close()
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.options[identity] = &option
		c.runtime[identity], err = c.connect(option)
		if err != nil {
			return
		}
	}()
	wg.Wait()
	err = common.SetTemporary(common.ConfigOption{
		Connect: c.options,
		Tunnel:  c.tunnels,
	})
	return
}

// Remotely execute commands via SSH
func (c *Client) Exec(identity string, cmd string) (output []byte, err error) {
	if c.options[identity] == nil || c.runtime[identity] == nil {
		err = errors.New("this identity does not exists")
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		session, err := c.runtime[identity].NewSession()
		if err != nil {
			return
		}
		defer session.Close()
		output, err = session.Output(cmd)
	}()
	wg.Wait()
	return
}

// Get ssh client information
func (c *Client) Get(identity string) (content Information, err error) {
	if c.options[identity] == nil || c.runtime[identity] == nil {
		err = errors.New("this identity does not exists")
		return
	}
	option := c.options[identity]
	var tunnels []common.TunnelOption
	if c.tunnels[identity] != nil {
		tunnels = *c.tunnels[identity]
	}
	content = Information{
		Identity:  identity,
		Host:      option.Host,
		Port:      option.Port,
		Username:  option.Username,
		Connected: string(c.runtime[identity].ClientVersion()),
		Tunnels:   tunnels,
	}
	return
}

// Delete ssh client
func (c *Client) Delete(identity string) (err error) {
	if c.options[identity] == nil || c.runtime[identity] == nil {
		return
	}
	if c.tunnels[identity] != nil {
		c.closeTunnel(identity)
	}
	if c.runtime[identity] != nil {
		c.runtime[identity].Close()
	}
	delete(c.runtime, identity)
	delete(c.options, identity)
	err = common.SetTemporary(common.ConfigOption{
		Connect: c.options,
		Tunnel:  c.tunnels,
	})
	return
}

// Tunnel setting
func (c *Client) SetTunnels(identity string, options []common.TunnelOption) (err error) {
	if c.options[identity] == nil || c.runtime[identity] == nil {
		err = errors.New("this identity does not exists")
		return
	}
	if c.tunnels[identity] != nil {
		c.closeTunnel(identity)
	}
	c.tunnels[identity] = &options
	for _, tunnel := range options {
		go c.mutilTunnel(identity, tunnel)
	}
	err = common.SetTemporary(common.ConfigOption{
		Connect: c.options,
		Tunnel:  c.tunnels,
	})
	return
}

// Close all running tunnels to which the identity belongs
func (c *Client) closeTunnel(identity string) {
	for _, conn := range c.remoteConn.Map[identity] {
		(*conn).Close()
	}
	c.remoteConn.Clear(identity)
	for _, conn := range c.localConn.Map[identity] {
		(*conn).Close()
	}
	c.localConn.Clear(identity)
	for _, listener := range c.localListener.Map[identity] {
		_ = (*listener).Close()
	}
	c.localListener.Clear(identity)
}

// Multiple tunnel settings
func (c *Client) mutilTunnel(identity string, option common.TunnelOption) {
	localAddr := common.GetAddr(option.DstIp, uint(option.DstPort))
	remoteAddr := common.GetAddr(option.SrcIp, uint(option.SrcPort))
	localListener, err := net.Listen("tcp", localAddr)
	if err != nil {
		println("<" + identity + ">:" + err.Error())
		return
	} else {
		c.localListener.Set(identity, localAddr, &localListener)
	}
	for {
		localConn, err := localListener.Accept()
		if err != nil {
			println("<" + identity + ">:" + err.Error())
			return
		} else {
			c.localConn.Set(identity, localAddr, &localConn)
		}
		remoteConn, err := c.runtime[identity].Dial("tcp", remoteAddr)
		if err != nil {
			println("remote <" + identity + ">:" + err.Error())
			return
		} else {
			c.remoteConn.Set(identity, localAddr, &remoteConn)
		}
		go c.forward(&localConn, &remoteConn)
	}
}

//  Tcp stream forwarding
func (c *Client) forward(local *net.Conn, remote *net.Conn) {
	defer (*local).Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		common.Copy(*local, *remote)
	}()
	go func() {
		defer wg.Done()
		if _, err := common.Copy(*remote, *local); err != nil {
			(*local).Close()
			(*remote).Close()
		}
		(*remote).Close()
	}()
	wg.Wait()
}