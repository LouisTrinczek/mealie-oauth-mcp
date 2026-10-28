package oauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultCIMDTimeout  = 5 * time.Second
	defaultCIMDMaxBytes = 64 << 10
	defaultCIMDCacheTTL = time.Hour
	cimdFailureTTL      = time.Minute
	maxCIMDCacheEntries = 1024
	maxCIMDHeaderBytes  = 16 << 10
)

var (
	errBlockedAddress = errors.New("address is not publicly routable")
	nat64Prefix       = netip.MustParsePrefix("64:ff9b::/96")
	blockedPrefixes   = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("fec0::/10"),
	}
)

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type CIMDOptions struct {
	Timeout      time.Duration
	MaxBytes     int64
	CacheTTL     time.Duration
	AllowPrivate bool
	Resolver     Resolver
	RootCAs      *x509.CertPool
}

type ClientMetadata struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
}

type CIMDFetcher struct {
	client     *http.Client
	maxBytes   int64
	cacheTTL   time.Duration
	now        func() time.Time
	maxEntries int
	mu         sync.Mutex
	cache      map[string]cimdEntry
}

type cimdEntry struct {
	metadata ClientMetadata
	err      error
	expires  time.Time
}

func NewCIMDFetcher(opts CIMDOptions) *CIMDFetcher {
	return newCIMDFetcher(opts, time.Now)
}

func newCIMDFetcher(opts CIMDOptions, now func() time.Time) *CIMDFetcher {
	opts = withCIMDDefaults(opts)
	return &CIMDFetcher{
		client:     newCIMDClient(opts),
		maxBytes:   opts.MaxBytes,
		cacheTTL:   opts.CacheTTL,
		now:        now,
		maxEntries: maxCIMDCacheEntries,
		cache:      make(map[string]cimdEntry),
	}
}

func withCIMDDefaults(opts CIMDOptions) CIMDOptions {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultCIMDTimeout
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultCIMDMaxBytes
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = defaultCIMDCacheTTL
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	return opts
}

func (f *CIMDFetcher) Fetch(ctx context.Context, clientID string) (ClientMetadata, error) {
	if err := validateClientIDURL(clientID); err != nil {
		return ClientMetadata{}, err
	}
	if entry, ok := f.cached(clientID); ok {
		return cloneMetadata(entry.metadata), entry.err
	}
	metadata, err := f.fetch(ctx, clientID)
	if ctx.Err() == nil {
		f.store(clientID, metadata, err)
	}
	return cloneMetadata(metadata), err
}

func validateClientIDURL(clientID string) error {
	if strings.Contains(clientID, "#") {
		return errors.New("client_id URL must not contain a fragment")
	}
	u, err := url.Parse(clientID)
	if err != nil {
		return fmt.Errorf("parsing client_id URL: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("client_id URL must be https with a host and no userinfo")
	}
	if u.Path == "" || u.Path == "/" {
		return errors.New("client_id URL must have a path")
	}
	return nil
}

func cloneMetadata(metadata ClientMetadata) ClientMetadata {
	metadata.RedirectURIs = slices.Clone(metadata.RedirectURIs)
	return metadata
}

func (f *CIMDFetcher) cached(clientID string) (cimdEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.cache[clientID]
	if !ok || !f.now().Before(entry.expires) {
		return cimdEntry{}, false
	}
	return entry, true
}

func (f *CIMDFetcher) store(clientID string, metadata ClientMetadata, err error) {
	ttl := f.cacheTTL
	if err != nil {
		ttl = cimdFailureTTL
	}
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evictExpired(now)
	if len(f.cache) >= f.maxEntries {
		return
	}
	f.cache[clientID] = cimdEntry{metadata: metadata, err: err, expires: now.Add(ttl)}
}

func (f *CIMDFetcher) evictExpired(now time.Time) {
	for clientID, entry := range f.cache {
		if !now.Before(entry.expires) {
			delete(f.cache, clientID)
		}
	}
}

func (f *CIMDFetcher) fetch(ctx context.Context, clientID string) (ClientMetadata, error) {
	body, err := f.download(ctx, clientID)
	if err != nil {
		return ClientMetadata{}, err
	}
	return parseCIMD(body, clientID)
}

func (f *CIMDFetcher) download(ctx context.Context, clientID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building client metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching client metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkCIMDResponse(resp); err != nil {
		return nil, err
	}
	return readLimited(resp.Body, f.maxBytes)
}

func checkCIMDResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("client metadata returned status %d, want 200", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("client metadata content type must be application/json")
	}
	return nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading client metadata: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("client metadata exceeds %d bytes", maxBytes)
	}
	return body, nil
}

type cimdDocument struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func parseCIMD(body []byte, clientID string) (ClientMetadata, error) {
	var doc cimdDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return ClientMetadata{}, fmt.Errorf("decoding client metadata: %w", err)
	}
	if err := doc.validate(clientID); err != nil {
		return ClientMetadata{}, err
	}
	return ClientMetadata{ClientID: doc.ClientID, ClientName: doc.ClientName, RedirectURIs: doc.RedirectURIs}, nil
}

func (d cimdDocument) validate(clientID string) error {
	if d.ClientID != clientID {
		return errors.New("client metadata client_id does not match the URL it was fetched from")
	}
	if len(d.RedirectURIs) == 0 {
		return errors.New("client metadata must list redirect_uris")
	}
	for _, uri := range d.RedirectURIs {
		if err := ValidateRedirectURIForRegistration(uri); err != nil {
			return fmt.Errorf("client metadata redirect_uris: %w", err)
		}
	}
	if d.TokenEndpointAuthMethod != "" && d.TokenEndpointAuthMethod != "none" {
		return errors.New("client metadata token_endpoint_auth_method must be none")
	}
	return nil
}

func newCIMDClient(opts CIMDOptions) *http.Client {
	dialer := guardedDialer{
		resolver: opts.Resolver,
		dialer: &net.Dialer{
			Timeout: opts.Timeout,
			Control: addressGuard{allowPrivate: opts.AllowPrivate}.control,
		},
	}
	transport := &http.Transport{
		DialContext:            dialer.DialContext,
		TLSClientConfig:        &tls.Config{RootCAs: opts.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    opts.Timeout,
		ResponseHeaderTimeout:  opts.Timeout,
		MaxResponseHeaderBytes: maxCIMDHeaderBytes,
		DisableKeepAlives:      true,
	}
	return &http.Client{Transport: transport, Timeout: opts.Timeout, CheckRedirect: refuseRedirect}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

type guardedDialer struct {
	resolver Resolver
	dialer   *net.Dialer
}

func (d guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("splitting dial address: %w", err)
	}
	addrs, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, dialFailure(host, lastErr)
}

func (d guardedDialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	addrs, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("could not resolve %s", host)
	}
	return addrs, nil
}

func dialFailure(host string, err error) error {
	if errors.Is(err, errBlockedAddress) {
		return fmt.Errorf("connecting to %s: %w", host, errBlockedAddress)
	}
	return fmt.Errorf("could not connect to %s", host)
}

type addressGuard struct {
	allowPrivate bool
}

func (g addressGuard) control(_, address string, _ syscall.RawConn) error {
	if g.allowPrivate {
		return nil
	}
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil || isBlockedAddr(addrPort.Addr()) {
		return errBlockedAddress
	}
	return nil
}

func isBlockedAddr(addr netip.Addr) bool {
	addr = embeddedIPv4(addr.WithZone("").Unmap())
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || addr.IsUnspecified() || inBlockedPrefix(addr)
}

func embeddedIPv4(addr netip.Addr) netip.Addr {
	if !nat64Prefix.Contains(addr) {
		return addr
	}
	bytes := addr.As16()
	return netip.AddrFrom4([4]byte(bytes[12:]))
}

func inBlockedPrefix(addr netip.Addr) bool {
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
