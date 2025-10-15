package acm

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// ProxyClient handles communication with ACM cluster-proxy API
type ProxyClient struct {
	httpClient     *http.Client
	serverURL      string
	bearerToken    string
	proxyRouteHost string // Dynamically discovered cluster-proxy route
}

// NewProxyClient creates a new ACM proxy client
func NewProxyClient(serverURL, bearerToken string) *ProxyClient {
	client := &ProxyClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // ACM typically uses self-signed certs
				},
			},
		},
		serverURL:   strings.TrimSuffix(serverURL, "/"),
		bearerToken: bearerToken,
	}

	// Dynamically discover the cluster-proxy route
	client.discoverProxyRoute()

	return client
}

// ProxyRequest makes a request to the specified cluster via ACM proxy
func (c *ProxyClient) ProxyRequest(ctx context.Context, cluster, apiPath string) (*http.Response, error) {
	// Use cluster-proxy-addon-user external route for direct API access to managed clusters
	// Format: https://<route-host>/<clusterName><apiPath>

	// Use dynamically discovered cluster-proxy route
	if c.proxyRouteHost == "" {
		return nil, fmt.Errorf("cluster-proxy route not discovered - ensure ACM cluster-proxy addon is installed")
	}

	// Build the route-based cluster proxy URL
	fullURL := fmt.Sprintf("https://%s/%s%s", c.proxyRouteHost, cluster, apiPath)

	klog.V(3).Infof("ACM proxy request: %s", fullURL)
	fmt.Printf("DEBUG: ACM proxy service URL: %s\n", fullURL)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy request: %w", err)
	}

	// Set authentication header
	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kubernetes-mcp-server/acm-proxy")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ACM proxy request failed for cluster %s: %w", cluster, err)
	}

	// Check for HTTP errors
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ACM proxy returned %d for cluster %s: %s",
			resp.StatusCode, cluster, string(body))
	}

	return resp, nil
}

// ProxyLogRequest makes a log request to the specified pod via ACM proxy
func (c *ProxyClient) ProxyLogRequest(ctx context.Context, cluster, namespace, pod, container string, tailLines int) (*http.Response, error) {
	// Build log-specific proxy URL
	// Format: /apis/proxy.open-cluster-management.io/v1beta1/namespaces/{cluster}/clusterstatuses/{cluster}/log/{namespace}/{pod}/{container}
	logPath := fmt.Sprintf("/apis/proxy.open-cluster-management.io/v1beta1/namespaces/%s/clusterstatuses/%s/log/%s/%s/%s",
		cluster, cluster, namespace, pod, container)

	// Add query parameters for log options
	u, err := url.Parse(c.serverURL + logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse log URL: %w", err)
	}

	q := u.Query()
	if tailLines > 0 {
		q.Set("tailLines", fmt.Sprintf("%d", tailLines))
	}
	u.RawQuery = q.Encode()

	klog.V(3).Infof("ACM proxy log request: %s", u.String())

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy log request: %w", err)
	}

	// Set authentication header
	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "kubernetes-mcp-server/acm-proxy")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ACM proxy log request failed for cluster %s: %w", cluster, err)
	}

	// Check for HTTP errors
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ACM proxy log returned %d for cluster %s: %s",
			resp.StatusCode, cluster, string(body))
	}

	return resp, nil
}

// ValidateCluster checks if the specified cluster is accessible via ACM proxy
func (c *ProxyClient) ValidateCluster(ctx context.Context, cluster string) error {
	// Try to access the cluster's API root via proxy
	resp, err := c.ProxyRequest(ctx, cluster, "/api/v1")
	if err != nil {
		return fmt.Errorf("cluster %s not accessible via ACM proxy: %w", cluster, err)
	}
	defer func() { _ = resp.Body.Close() }()

	klog.V(2).Infof("Cluster %s validated via ACM proxy", cluster)
	return nil
}

// IsACMEnvironment checks if we're running in an ACM environment
func (c *ProxyClient) IsACMEnvironment(ctx context.Context) bool {
	// Check for ACM APIs availability
	checkURL := c.serverURL + "/apis/cluster.open-cluster-management.io/v1"

	req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bearer "+c.bearerToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == 200
}

// ListManagedClusters returns a list of available managed clusters
func (c *ProxyClient) ListManagedClusters(ctx context.Context) ([]string, error) {
	checkURL := c.serverURL + "/apis/cluster.open-cluster-management.io/v1/managedclusters"

	req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed clusters request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list managed clusters: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to list managed clusters, status: %d", resp.StatusCode)
	}

	// TODO: Parse the JSON response to extract cluster names
	// For now, return empty list - this will be implemented when we add JSON parsing
	klog.V(2).Info("Successfully connected to ACM managed clusters API")
	return []string{}, nil
}

// discoverProxyRoute dynamically discovers the cluster-proxy-user route
func (c *ProxyClient) discoverProxyRoute() {
	// Try to get the cluster-proxy-user route from the multicluster-engine namespace
	routeURL := c.serverURL + "/apis/route.openshift.io/v1/namespaces/multicluster-engine/routes/cluster-proxy-addon-user"

	req, err := http.NewRequest("GET", routeURL, nil)
	if err != nil {
		klog.V(2).Infof("Failed to create route discovery request: %v", err)
		return
	}

	req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		klog.V(2).Infof("Failed to discover cluster-proxy route: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		klog.V(2).Infof("Failed to get cluster-proxy route, status: %d", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		klog.V(2).Infof("Failed to read route response: %v", err)
		return
	}

	// Parse the route spec.host field from the JSON response
	// Simple extraction - in production, would use proper JSON parsing
	route := parseRouteHost(string(body))
	if route != "" {
		c.proxyRouteHost = route
		klog.V(2).Infof("Discovered cluster-proxy route: %s", route)
	} else {
		klog.V(2).Info("Could not extract route host from response")
	}
}

// parseRouteHost extracts the host from a route JSON response
func parseRouteHost(jsonResponse string) string {
	// Simple string parsing to extract spec.host field
	// Looking for: "spec":{"host":"cluster-proxy-user.apps.domain.com"
	if idx := strings.Index(jsonResponse, `"spec":`); idx != -1 {
		specPart := jsonResponse[idx:]
		if hostIdx := strings.Index(specPart, `"host":"`); hostIdx != -1 {
			hostStart := hostIdx + 8 // len(`"host":"`)
			hostPart := specPart[hostStart:]
			if endIdx := strings.Index(hostPart, `"`); endIdx != -1 {
				return hostPart[:endIdx]
			}
		}
	}
	return ""
}
