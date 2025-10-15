package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	internalk8s "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	"github.com/containers/kubernetes-mcp-server/pkg/output"
	"github.com/google/jsonschema-go/jsonschema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ServerTool struct {
	Tool    Tool
	Handler ToolHandlerFunc
}

type Toolset interface {
	// GetName returns the name of the toolset.
	// Used to identify the toolset in configuration, logs, and command-line arguments.
	// Examples: "core", "metrics", "helm"
	GetName() string
	GetDescription() string
	GetTools(o internalk8s.Openshift) []ServerTool
}

type ToolCallRequest interface {
	GetArguments() map[string]any
}

type ToolCallResult struct {
	// Raw content returned by the tool.
	Content string
	// Error (non-protocol) to send back to the LLM.
	Error error
}

func NewToolCallResult(content string, err error) *ToolCallResult {
	return &ToolCallResult{
		Content: content,
		Error:   err,
	}
}

type ToolHandlerParams struct {
	context.Context
	*internalk8s.Kubernetes
	ToolCallRequest
	ListOutput output.Output
	// Multi-cluster support
	ACMProxyClient interface{} // ACM proxy client for multi-cluster operations
	IsACMMode      bool        // Whether ACM multi-cluster mode is enabled
}

type ToolHandlerFunc func(params ToolHandlerParams) (*ToolCallResult, error)

type Tool struct {
	// The name of the tool.
	// Intended for programmatic or logical use, but used as a display name in past
	// specs or fallback (if title isn't present).
	Name string `json:"name"`
	// A human-readable description of the tool.
	//
	// This can be used by clients to improve the LLM's understanding of available
	// tools. It can be thought of like a "hint" to the model.
	Description string `json:"description,omitempty"`
	// Additional tool information.
	Annotations ToolAnnotations `json:"annotations"`
	// A JSON Schema object defining the expected parameters for the tool.
	InputSchema *jsonschema.Schema
}

type ToolAnnotations struct {
	// Human-readable title for the tool
	Title string `json:"title,omitempty"`
	// If true, the tool does not modify its environment.
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
	// If true, the tool may perform destructive updates to its environment. If
	// false, the tool performs only additive updates.
	//
	// (This property is meaningful only when ReadOnlyHint == false.)
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	// If true, calling the tool repeatedly with the same arguments will have no
	// additional effect on its environment.
	//
	// (This property is meaningful only when ReadOnlyHint == false.)
	IdempotentHint *bool `json:"idempotentHint,omitempty"`
	// If true, this tool may interact with an "open world" of external entities. If
	// false, the tool's domain of interaction is closed. For example, the world of
	// a web search tool is open, whereas that of a memory tool is not.
	OpenWorldHint *bool `json:"openWorldHint,omitempty"`
}

// GetClusterParameter extracts the optional cluster parameter from tool arguments
func GetClusterParameter(params ToolHandlerParams) (string, bool) {
	args := params.GetArguments()
	if cluster, exists := args["cluster"]; exists {
		if clusterStr, ok := cluster.(string); ok && clusterStr != "" {
			return clusterStr, true
		}
	}
	return "", false
}

// ShouldUseACMProxy returns true if the request should be routed through ACM proxy
func ShouldUseACMProxy(params ToolHandlerParams) (string, bool) {
	if !params.IsACMMode || params.ACMProxyClient == nil {
		return "", false
	}
	// Now using correct cluster-proxy-addon-user service approach for all operations
	return GetClusterParameter(params)
}

// ShouldUseACMProxyForLogs returns true if pod logs should be routed through ACM proxy
func ShouldUseACMProxyForLogs(params ToolHandlerParams) (string, bool) {
	if !params.IsACMMode || params.ACMProxyClient == nil {
		return "", false
	}
	return GetClusterParameter(params)
}

func ToRawMessage(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Shadow methods for ToolHandlerParams that add ACM proxy routing

// ResourcesList routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) ResourcesList(ctx context.Context, gvk *schema.GroupVersionKind, namespace string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	fmt.Printf("DEBUG: ResourcesList shadow method called with gvk=%v, namespace=%s\n", gvk, namespace)
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		fmt.Printf("DEBUG: Using ACM proxy for cluster=%s\n", cluster)
		return p.routeResourcesListThroughProxy(ctx, cluster, gvk, namespace, options)
	}
	fmt.Printf("DEBUG: Using direct Kubernetes client\n")
	return p.Kubernetes.ResourcesList(ctx, gvk, namespace, options)
}

// ResourcesGet routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) ResourcesGet(ctx context.Context, gvk *schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		return p.routeResourcesGetThroughProxy(ctx, cluster, gvk, namespace, name)
	}
	return p.Kubernetes.ResourcesGet(ctx, gvk, namespace, name)
}

// ResourcesCreateOrUpdate routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) ResourcesCreateOrUpdate(ctx context.Context, resource string) ([]*unstructured.Unstructured, error) {
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		return p.routeResourcesCreateOrUpdateThroughProxy(ctx, cluster, resource)
	}
	return p.Kubernetes.ResourcesCreateOrUpdate(ctx, resource)
}

// ResourcesDelete routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) ResourcesDelete(ctx context.Context, gvk *schema.GroupVersionKind, namespace, name string) error {
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		return p.routeResourcesDeleteThroughProxy(ctx, cluster, gvk, namespace, name)
	}
	return p.Kubernetes.ResourcesDelete(ctx, gvk, namespace, name)
}

// Pod-specific shadow methods

// PodsListInNamespace routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) PodsListInNamespace(ctx context.Context, namespace string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	fmt.Printf("DEBUG: PodsListInNamespace shadow method called with namespace=%s\n", namespace)
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		fmt.Printf("DEBUG: Using ACM proxy for cluster=%s\n", cluster)
		return p.routePodsListInNamespaceThroughProxy(ctx, cluster, namespace, options)
	}
	fmt.Printf("DEBUG: Using direct Kubernetes client\n")
	return p.Kubernetes.PodsListInNamespace(ctx, namespace, options)
}

// PodsListInAllNamespaces routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) PodsListInAllNamespaces(ctx context.Context, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		return p.routePodsListInAllNamespacesThroughProxy(ctx, cluster, options)
	}
	return p.Kubernetes.PodsListInAllNamespaces(ctx, options)
}

// NamespacesList routes through ACM proxy when cluster parameter is provided
func (p ToolHandlerParams) NamespacesList(ctx context.Context, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	if cluster, shouldUse := ShouldUseACMProxy(p); shouldUse {
		return p.routeNamespacesListThroughProxy(ctx, cluster, options)
	}
	return p.Kubernetes.NamespacesList(ctx, options)
}

// Direct proxy methods for handlers to call
func (p ToolHandlerParams) PodsListInNamespaceThroughProxy(ctx context.Context, cluster, namespace string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	return p.routePodsListInNamespaceThroughProxy(ctx, cluster, namespace, options)
}

// Helper methods for ACM proxy routing

func (p ToolHandlerParams) routeResourcesListThroughProxy(ctx context.Context, cluster string, gvk *schema.GroupVersionKind, namespace string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	// Build Kubernetes API path for list operation
	var apiPath string
	if len(gvk.Group) == 0 {
		apiPath = fmt.Sprintf("/api/%s", gvk.Version)
	} else {
		apiPath = fmt.Sprintf("/apis/%s/%s", gvk.Group, gvk.Version)
	}

	// Add namespace if provided
	if namespace != "" {
		apiPath = fmt.Sprintf("%s/namespaces/%s", apiPath, namespace)
	}

	// Add resource type (convert Kind to resource name)
	resourceName := p.kindToResourceName(gvk.Kind)
	apiPath = fmt.Sprintf("%s/%s", apiPath, resourceName)

	// Add query parameters
	if options.LabelSelector != "" {
		apiPath = fmt.Sprintf("%s?labelSelector=%s", apiPath, options.LabelSelector)
	}

	return p.makeProxyRequest(ctx, cluster, apiPath)
}

func (p ToolHandlerParams) routeResourcesGetThroughProxy(ctx context.Context, cluster string, gvk *schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	// Build Kubernetes API path for get operation
	var apiPath string
	if len(gvk.Group) == 0 {
		apiPath = fmt.Sprintf("/api/%s", gvk.Version)
	} else {
		apiPath = fmt.Sprintf("/apis/%s/%s", gvk.Group, gvk.Version)
	}

	// Add namespace if provided
	if namespace != "" {
		apiPath = fmt.Sprintf("%s/namespaces/%s", apiPath, namespace)
	}

	// Add resource type and name
	resourceName := p.kindToResourceName(gvk.Kind)
	apiPath = fmt.Sprintf("%s/%s/%s", apiPath, resourceName, name)

	obj, err := p.makeProxyRequest(ctx, cluster, apiPath)
	if err != nil {
		return nil, err
	}

	// Convert to *unstructured.Unstructured
	if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
		return unstructuredObj, nil
	}

	return nil, fmt.Errorf("unexpected response type from proxy")
}

func (p ToolHandlerParams) routeResourcesCreateOrUpdateThroughProxy(ctx context.Context, cluster string, resource string) ([]*unstructured.Unstructured, error) {
	// For now, return an error as this requires more complex implementation
	return nil, fmt.Errorf("create/update operations via ACM proxy not yet implemented")
}

func (p ToolHandlerParams) routeResourcesDeleteThroughProxy(ctx context.Context, cluster string, gvk *schema.GroupVersionKind, namespace, name string) error {
	// For now, return an error as this requires more complex implementation
	return fmt.Errorf("delete operations via ACM proxy not yet implemented")
}

// Pod-specific proxy routing methods

func (p ToolHandlerParams) routePodsListInNamespaceThroughProxy(ctx context.Context, cluster string, namespace string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	// Build Kubernetes API path for pod list in namespace
	apiPath := fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace)

	// Add query parameters
	if options.LabelSelector != "" {
		apiPath = fmt.Sprintf("%s?labelSelector=%s", apiPath, options.LabelSelector)
	}

	fmt.Printf("DEBUG: Constructed API path: %s\n", apiPath)
	return p.makeProxyRequest(ctx, cluster, apiPath)
}

func (p ToolHandlerParams) routePodsListInAllNamespacesThroughProxy(ctx context.Context, cluster string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	// Build Kubernetes API path for pod list in all namespaces
	apiPath := "/api/v1/pods"

	// Add query parameters
	if options.LabelSelector != "" {
		apiPath = fmt.Sprintf("%s?labelSelector=%s", apiPath, options.LabelSelector)
	}

	return p.makeProxyRequest(ctx, cluster, apiPath)
}

func (p ToolHandlerParams) routeNamespacesListThroughProxy(ctx context.Context, cluster string, options internalk8s.ResourceListOptions) (runtime.Unstructured, error) {
	// Build Kubernetes API path for namespace list
	apiPath := "/api/v1/namespaces"

	// Add query parameters
	if options.LabelSelector != "" {
		apiPath = fmt.Sprintf("%s?labelSelector=%s", apiPath, options.LabelSelector)
	}

	return p.makeProxyRequest(ctx, cluster, apiPath)
}

func (p ToolHandlerParams) makeProxyRequest(ctx context.Context, cluster, apiPath string) (runtime.Unstructured, error) {
	// Cast ACMProxyClient to the actual ProxyClient type
	type ProxyClient interface {
		ProxyRequest(ctx context.Context, cluster, apiPath string) (*http.Response, error)
	}

	proxyClient, ok := p.ACMProxyClient.(ProxyClient)
	if !ok {
		return nil, fmt.Errorf("ACMProxyClient does not implement ProxyRequest method")
	}

	// Make the proxy request
	resp, err := proxyClient.ProxyRequest(ctx, cluster, apiPath)
	if err != nil {
		return nil, fmt.Errorf("ACM proxy request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ACM proxy response: %w", err)
	}

	// Parse JSON response to unstructured object
	var obj unstructured.Unstructured
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("failed to parse ACM proxy response: %w", err)
	}

	return &obj, nil
}

func (p ToolHandlerParams) kindToResourceName(kind string) string {
	// Simple kind to resource name conversion
	// This is a basic implementation - in production, you'd want to use discovery
	switch kind {
	case "Pod":
		return "pods"
	case "Service":
		return "services"
	case "Deployment":
		return "deployments"
	case "Namespace":
		return "namespaces"
	case "Node":
		return "nodes"
	case "ConfigMap":
		return "configmaps"
	case "Secret":
		return "secrets"
	case "Event":
		return "events"
	case "ReplicaSet":
		return "replicasets"
	case "StatefulSet":
		return "statefulsets"
	case "DaemonSet":
		return "daemonsets"
	case "Ingress":
		return "ingresses"
	case "PersistentVolume":
		return "persistentvolumes"
	case "PersistentVolumeClaim":
		return "persistentvolumeclaims"
	case "ServiceAccount":
		return "serviceaccounts"
	case "Role":
		return "roles"
	case "RoleBinding":
		return "rolebindings"
	case "ClusterRole":
		return "clusterroles"
	case "ClusterRoleBinding":
		return "clusterrolebindings"
	default:
		// Convert CamelCase to lowercase plural (naive approach)
		// This is a fallback for unknown kinds
		lower := ""
		for i, r := range kind {
			if i > 0 && r >= 'A' && r <= 'Z' {
				lower += string(r - 'A' + 'a')
			} else {
				lower += string(r)
			}
		}
		return lower + "s"
	}
}
