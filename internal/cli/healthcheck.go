package cli

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// cmdHealthcheck probes this host's own HTTP listener, for a container health
// check or a supervisor that has no curl. It resolves server.http_addr and
// security.tls_enabled the way serve does, so the same flags, environment, and
// config file that started the server also describe how to reach it.
//
// The probe is deliberately loopback-only and does not verify the server's
// certificate: it answers "is this process still serving?" from inside the
// container, where the certificate's name almost never matches 127.0.0.1 and
// where there is no man in the middle to defend against. It is not a way to
// check a remote server.
func (c *CLI) cmdHealthcheck(args []string) int {
	fs := c.newFlags("healthcheck")
	r := c.serverSettings(fs, "server.http_addr", "security.tls_enabled")
	ready := fs.Bool("ready", false, "probe /readyz (store baseline and master key) instead of /healthz")
	timeout := fs.Duration("timeout", 3*time.Second, "give up after this `duration`")
	c.setUsage(fs, "healthcheck [flags]",
		"Probe this host's own HTTP listener on 127.0.0.1 and exit 0 when it answers 200. "+
			"The server's certificate is not verified: this is a loopback liveness check for a "+
			"container HEALTHCHECK or a process supervisor, not a way to check a remote server.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, _, _, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}

	_, port, err := net.SplitHostPort(cfg.Server.HTTPAddr)
	if err != nil {
		return c.fail("server.http_addr %q is not host:port: %v", cfg.Server.HTTPAddr, err)
	}
	scheme := "http"
	client := &http.Client{Timeout: *timeout}
	if cfg.Security.TLSEnabled {
		scheme = "https"
		// InsecureSkipVerify is deliberate and safe here: the connection never
		// leaves the loopback interface, and the server certificate is issued
		// for the deployment's hostname, not for 127.0.0.1.
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		}
	}
	path := "/healthz"
	if *ready {
		path = "/readyz"
	}
	url := scheme + "://" + net.JoinHostPort("127.0.0.1", port) + path

	resp, err := client.Get(url)
	if err != nil {
		return c.fail("%s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c.fail("%s: HTTP %d", url, resp.StatusCode)
	}
	return 0
}
