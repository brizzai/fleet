package chrome

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
)

// Client communicates with the Chrome extension via the native host's Unix socket.
type Client struct{}

// connState tracks the latest reachability of the Chrome extension so we emit
// analytics events only on state transitions, not on every Send.
var (
	connStateMu        sync.Mutex
	connStateSeen      bool // have we observed any state yet this process?
	connStateLastWasOK bool
)

func recordConnState(ok bool) {
	connStateMu.Lock()
	wasSeen := connStateSeen
	last := connStateLastWasOK
	connStateSeen = true
	connStateLastWasOK = ok
	connStateMu.Unlock()

	switch {
	case !wasSeen && ok:
		analytics.Track(analytics.EventChromeExtensionConnected, nil)
	case wasSeen && last && !ok:
		analytics.Track(analytics.EventChromeExtensionDisconnected, nil)
	case wasSeen && !last && ok:
		analytics.Track(analytics.EventChromeExtensionConnected, nil)
	}
}

// Send sends a command to the Chrome extension and waits for a response.
// Returns an error if the socket doesn't exist (extension not running).
func (c *Client) Send(cmd *Command) (*Response, error) {
	sockPath := SocketPath()

	conn, err := net.DialTimeout("unix", sockPath, 3*time.Second)
	if err != nil {
		recordConnState(false)
		return nil, fmt.Errorf("chrome extension not available: %w", err)
	}
	defer conn.Close()
	recordConnState(true)

	// Set read deadline for response.
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send command.
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write command: %w", err)
	}

	// Signal we're done writing so the host can ReadAll.
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}

	// Read response.
	buf := make([]byte, 64*1024)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !resp.Success {
		return &resp, fmt.Errorf("chrome: %s", resp.Error)
	}

	return &resp, nil
}
