package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/net/proxy"

	"github.com/blacknon/lssh/conf"
)

// Connect structure to store contents about ssh connection.
type Connect struct {
	// Name of server to connect.
	// It plays an important role in obtaining connection information from Configure.
	Server string

	// conf/Config Structure.
	Conf   conf.Config
	Client *ssh.Client

	// ssh-agent interface.
	// TODO(blacknon): Integrate later.
	sshAgent         agent.Agent
	sshExtendedAgent agent.ExtendedAgent

	// connect login shell flag
	IsTerm bool

	// parallel connect flag
	IsParallel bool

	// use local bashrc flag
	IsLocalRc bool

	// local bashrc data
	LocalRcData string

	// local bashrc decode command
	LocalRcDecodeCmd string

	// port forward setting.`host:port`
	ForwardLocal  string
	ForwardRemote string

	// x11 forward setting.
	X11 bool

	// AuthMap
	AuthMap map[AuthKey][]ssh.Signer
}

type Proxy struct {
	Name string
	Type string
}

// SendKeepAlive send KeepAlive packet from specified Session.
func (c *Connect) SendKeepAlive(session *ssh.Session) {
	for {
		_, _ = session.SendRequest("keepalive@lssh.com", true, nil)
		time.Sleep(15 * time.Second)
	}
}

// CheckClientAlive Check alive ssh.Client.
func (c *Connect) CheckClientAlive() error {
	_, _, err := c.Client.SendRequest("keepalive@lssh.com", true, nil)
	if err == nil || err.Error() == "request failed" {
		return nil
	}
	return err
}

// CreateSession return *ssh.Session
func (c *Connect) CreateSession() (session *ssh.Session, err error) {
	// new connect
	if c.Client == nil {
		err = c.CreateClient()
		if err != nil {
			return session, err
		}
	}

	// Check ssh client alive
	clientErr := c.CheckClientAlive()
	if clientErr != nil {
		err = c.CreateClient()
		if err != nil {
			return session, err
		}
	}

	// New session
	session, err = c.Client.NewSession()

	if err != nil {
		return session, err
	}

	return
}

// CreateClient create ssh.Client and store in Connect.Client
func (c *Connect) CreateClient() (err error) {
	// New ClientConfig
	serverConf := c.Conf.Server[c.Server]

	// if use ssh-agent
	if serverConf.SSHAgentUse || serverConf.AgentAuth {
		err := c.CreateSshAgent()
		if err != nil {
			return err
		}
	}

	sshConf, err := c.createClientConfig(c.Server)
	if err != nil {
		return err
	}

	// set default port 22
	if serverConf.Port == "" {
		serverConf.Port = "22"
	}

	// not use proxy
	if serverConf.Proxy == "" && serverConf.ProxyCommand == "" {
		client, err := ssh.Dial("tcp", net.JoinHostPort(serverConf.Addr, serverConf.Port), sshConf)
		if err != nil {
			return err
		}

		// set client
		c.Client = client
	} else {
		err := c.createClientOverProxy(serverConf, sshConf)
		if err != nil {
			return err
		}
	}

	c.X11 = serverConf.X11

	return err
}

// createClientOverProxy create over multiple proxy ssh.Client, and store in Connect.Client
func (c *Connect) createClientOverProxy(serverConf conf.ServerConfig, sshConf *ssh.ClientConfig) (err error) {
	// get proxy slice
	proxyList, proxyType, err := GetProxyList(c.Server, c.Conf)
	if err != nil {
		return err
	}

	// var
	var proxyClient *ssh.Client
	var proxyDialer proxy.Dialer

	for _, proxy := range proxyList {
		switch proxyType[proxy] {
		case "http", "https":
			proxyConf := c.Conf.Proxy[proxy]
			proxyDialer, err = createProxyDialerHttp(proxyConf)

		case "socks5":
			proxyConf := c.Conf.Proxy[proxy]
			proxyDialer, err = createProxyDialerSocks5(proxyConf)

		default:
			proxyConf := c.Conf.Server[proxy]
			proxySshConf, err := c.createClientConfig(proxy)
			if err != nil {
				return err
			}
			proxyClient, err = createClientViaProxy(proxyConf, proxySshConf, proxyClient, proxyDialer)

		}

		if err != nil {
			return err
		}
	}

	client, err := createClientViaProxy(serverConf, sshConf, proxyClient, proxyDialer)
	if err != nil {
		return err
	}

	// set c.client
	c.Client = client

	return
}

// createClientConfig return *ssh.ClientConfig
func (c *Connect) createClientConfig(server string) (clientConfig *ssh.ClientConfig, err error) {
	conf := c.Conf.Server[server]

	auth, err := c.createSshAuth(server)
	if err != nil {
		if len(auth) == 0 {
			return clientConfig, err
		}
	}

	// create ssh ClientConfig
	clientConfig = &ssh.ClientConfig{
		User:            conf.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	return clientConfig, err
}

// RunCmd execute command via ssh from specified session.
func (c *Connect) RunCmd(session *ssh.Session, command []string) (err error) {
	defer session.Close()

	// set TerminalModes
	if session, err = c.setIsTerm(session); err != nil {
		return
	}

	// join command
	execCmd := strings.Join(command, " ")

	// run command
	isExit := make(chan bool)
	go func() {
		err = session.Run(execCmd)
		isExit <- true
	}()

	// check command exit
CheckCommandExit:
	for {
		// time.Sleep(100 * time.Millisecond)
		select {
		case <-isExit:
			break CheckCommandExit
		case <-time.After(10 * time.Millisecond):
			continue CheckCommandExit
		}
	}

	return
}

// RunCmdWithOutput execute a command via ssh from the specified session and send its output to outputchan.
func (c *Connect) RunCmdWithOutput(session *ssh.Session, command []string, outputChan chan []byte) {
	outputBuf := new(bytes.Buffer)
	session.Stdout = io.MultiWriter(outputBuf)
	session.Stderr = io.MultiWriter(outputBuf)

	// run command
	isExit := make(chan bool)
	go func() {
		c.RunCmd(session, command)
		isExit <- true
	}()

GetOutputLoop:
	for {
		if outputBuf.Len() > 0 {
			line, _ := outputBuf.ReadBytes('\n')
			outputChan <- line
		} else {
			select {
			case <-isExit:
				break GetOutputLoop
			case <-time.After(10 * time.Millisecond):
				continue GetOutputLoop
			}
		}
	}

	// last check
	if outputBuf.Len() > 0 {
		for {
			line, err := outputBuf.ReadBytes('\n')
			if err != io.EOF {
				outputChan <- line
			} else {
				break
			}
		}
	}
}

// ConTerm connect to a shell using a terminal.
func (c *Connect) ConTerm(session *ssh.Session) (err error) {
	// defer session.Close()
	fd := int(os.Stdin.Fd())
	state, err := terminal.MakeRaw(fd)
	if err != nil {
		return
	}
	defer terminal.Restore(fd, state)

	// get terminal size
	width, height, err := terminal.GetSize(fd)
	if err != nil {
		return
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	term := os.Getenv("TERM")
	err = session.RequestPty(term, height, width, modes)
	if err != nil {
		return
	}

	// start shell
	if c.IsLocalRc {
		session, err = c.runLocalRcShell(session)
		if err != nil {
			return
		}
	} else {
		err = session.Shell()
		if err != nil {
			return
		}
	}

	// Terminal resize
	if runtime.GOOS != "windows" {
		signal_chan := make(chan os.Signal, 1)
		signal.Notify(signal_chan, syscall.Signal(0x1c))
		go func() {
			for {
				s := <-signal_chan
				switch s {
				case syscall.Signal(0x1c):
					fd := int(os.Stdout.Fd())
					width, height, _ = terminal.GetSize(fd)
					session.WindowChange(height, width)
				}
			}
		}()
	}

	// keep alive packet
	go c.SendKeepAlive(session)

	err = session.Wait()
	if err != nil {
		return
	}

	return
}

// setIsTerm Enable tty(pesudo) when executing command over ssh.
func (c *Connect) setIsTerm(preSession *ssh.Session) (session *ssh.Session, err error) {
	if c.IsTerm {
		modes := ssh.TerminalModes{
			ssh.ECHO:          0,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}

		// Get terminal window size
		fd := int(os.Stdin.Fd())
		width, hight, err := terminal.GetSize(fd)
		if err != nil {
			preSession.Close()
			return session, err
		}

		term := os.Getenv("TERM")
		if err = preSession.RequestPty(term, hight, width, modes); err != nil {
			preSession.Close()
			return session, err
		}
	}
	session = preSession
	return
}

// GetProxyList return proxy list and map by proxy type.
func GetProxyList(server string, config conf.Config) (proxyList []string, proxyType map[string]string, err error) {
	var targetType string
	var preProxy, preProxyType string

	targetServer := server
	proxyType = map[string]string{}

	for {
		isOk := false

		switch targetType {
		case "http", "https", "socks5":
			_, isOk = config.Proxy[targetServer]
			preProxy = ""
			preProxyType = ""

		default:
			var preProxyConf conf.ServerConfig
			preProxyConf, isOk = config.Server[targetServer]
			preProxy = preProxyConf.Proxy
			preProxyType = preProxyConf.ProxyType
		}

		// not use pre proxy
		if preProxy == "" {
			break
		}

		if !isOk {
			err = fmt.Errorf("Not Found proxy : %s", targetServer)
			return nil, nil, err
		}

		// set proxy info
		proxy := new(Proxy)
		proxy.Name = preProxy

		switch preProxyType {
		case "http", "https", "socks5":
			proxy.Type = preProxyType
		default:
			proxy.Type = "ssh"
		}

		proxyList = append(proxyList, proxy.Name)
		proxyType[proxy.Name] = proxy.Type

		targetServer = proxy.Name
		targetType = proxy.Type
	}

	// reverse proxyServers slice
	for i, j := 0, len(proxyList)-1; i < j; i, j = i+1, j-1 {
		proxyList[i], proxyList[j] = proxyList[j], proxyList[i]
	}

	return
}

// runLocalRcShell connect to remote shell using local bashrc
func (c *Connect) runLocalRcShell(preSession *ssh.Session) (session *ssh.Session, err error) {
	session = preSession

	// command
	cmd := fmt.Sprintf("bash --rcfile <(echo %s|((base64 --help | grep -q coreutils) && base64 -d <(cat) || base64 -D <(cat) ))", c.LocalRcData)

	// decode command
	if len(c.LocalRcDecodeCmd) > 0 {
		cmd = fmt.Sprintf("bash --rcfile <(echo %s | %s)", c.LocalRcData, c.LocalRcDecodeCmd)
	}

	err = session.Start(cmd)

	return session, err
}
