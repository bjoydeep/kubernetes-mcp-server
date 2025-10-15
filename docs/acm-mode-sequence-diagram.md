# ACM Mode vs Non-ACM Mode Sequence Diagram

This document illustrates the code flow differences between ACM mode enabled and disabled scenarios when processing multi-cluster requests.

## ACM Mode Enabled Flow

```mermaid
sequenceDiagram
    participant Client
    participant MCPServer
    participant ToolHandler
    participant ACMClusterProxy
    participant ManagedClusterKubeAPI as Managed Cluster Kube API
    participant LocalClusterKubeAPI as Local Cluster Kube API

    Client->>MCPServer: Request with cluster="jb-mc-1"
    MCPServer->>ToolHandler: Execute tool (params.IsACMMode=true)
    ToolHandler->>ToolHandler: GetClusterParameter() → "jb-mc-1", true
    ToolHandler->>ToolHandler: ShouldUseACMProxy() → "jb-mc-1", true
    ToolHandler->>ACMClusterProxy: ProxyRequest(cluster="jb-mc-1", apiPath)
    ACMClusterProxy->>ACMClusterProxy: Build proxy URL:<br/>https://cluster-proxy-user.apps.{acm-domain}/jb-mc-1{apiPath}
    ACMClusterProxy->>ManagedClusterKubeAPI: HTTP Request via cluster-proxy-user route
    ManagedClusterKubeAPI-->>ACMClusterProxy: Response (pods, resources, etc.)
    ACMClusterProxy-->>ToolHandler: Proxy Response
    ToolHandler-->>MCPServer: Tool Result from jb-mc-1
    MCPServer-->>Client: Response with data from jb-mc-1
```

## ACM Mode Disabled Flow (Current Implementation)

```mermaid
sequenceDiagram
    participant Client
    participant MCPServer
    participant ToolHandler
    participant LocalClusterKubeAPI as Local Cluster Kube API

    Client->>MCPServer: Request with cluster="jb-mc-1"
    MCPServer->>ToolHandler: Execute tool (params.IsACMMode=false)
    ToolHandler->>ToolHandler: GetClusterParameter() → "jb-mc-1", true
    ToolHandler->>ToolHandler: ShouldUseACMProxy() → "", false
    Note over ToolHandler: ⚠️ PROBLEM: cluster parameter silently ignored!
    ToolHandler->>LocalClusterKubeAPI: Execute request on local cluster
    LocalClusterKubeAPI-->>ToolHandler: Response from LOCAL cluster
    ToolHandler-->>MCPServer: Tool Result from LOCAL cluster
    MCPServer-->>Client: Response with data from LOCAL cluster
    Note over Client: ⚠️ User thinks data is from jb-mc-1<br/>but it's actually from local cluster!
```

## Proposed Enhanced Flow (Non-ACM Mode with Validation)

```mermaid
sequenceDiagram
    participant Client
    participant MCPServer
    participant ToolHandler
    participant LocalClusterKubeAPI as Local Cluster Kube API

    Client->>MCPServer: Request with cluster="jb-mc-1"
    MCPServer->>ToolHandler: Execute tool (params.IsACMMode=false)
    ToolHandler->>ToolHandler: GetClusterParameter() → "jb-mc-1", true
    ToolHandler->>ToolHandler: Validate: cluster param provided but ACM mode disabled
    ToolHandler-->>MCPServer: Error: "cluster parameter 'jb-mc-1' provided<br/>but ACM mode is not enabled.<br/>Use --acm-mode flag to enable multi-cluster operations"
    MCPServer-->>Client: Clear error message
    Note over Client: ✅ User understands the issue<br/>and knows how to fix it
```

## Key Differences

| Scenario | Cluster Parameter | ACM Mode | Behavior | Risk Level |
|----------|------------------|----------|----------|------------|
| **ACM Enabled** | ✅ Provided | ✅ Enabled | Routes to target cluster | ✅ Safe |
| **ACM Disabled (Current)** | ✅ Provided | ❌ Disabled | **Silently uses local cluster** | 🚨 **Dangerous** |
| **ACM Disabled (Proposed)** | ✅ Provided | ❌ Disabled | **Returns clear error** | ✅ Safe |
| **No Cluster Param** | ❌ Not provided | Either | Uses local cluster | ✅ Safe |

## Code Locations

- **Flag Definition**: `pkg/kubernetes-mcp-server/cmd/root.go:137`
- **ACM Proxy Logic**: `pkg/api/toolsets.go:105-110`
- **ACM Proxy Client**: `pkg/acm/client.go`
- **Tool Handlers**: `pkg/toolsets/core/*.go`

## Security Implications

The current silent fallback behavior poses security risks:

1. **Unintended Operations**: Users might accidentally modify/delete resources on the wrong cluster
2. **Data Leakage**: Sensitive information from the local cluster could be exposed when users expect data from a different cluster
3. **Compliance Issues**: Operations intended for non-production clusters might be executed on production environments

The proposed validation approach eliminates these risks by failing fast with clear error messages.

## ✅ WORKING IMPLEMENTATION (Updated 2025-01-14)

### Successful ACM Cluster-Proxy Integration

After extensive debugging and testing, the correct ACM cluster-proxy implementation has been discovered and implemented:

**🔧 Key Breakthrough: Service-Based Routing Pattern**

```mermaid
sequenceDiagram
    participant Client
    participant MCPServer
    participant ToolHandler
    participant ACMProxyClient
    participant ClusterProxyRoute as cluster-proxy-user Route
    participant ProxyAgent as Proxy Agent (jb-mc-1)
    participant ManagedKubeAPI as Managed Cluster Kube API

    Client->>MCPServer: pods_list_in_namespace(namespace="stackrox", cluster="jb-mc-1")
    MCPServer->>ToolHandler: Execute tool (params.IsACMMode=true, ACMProxyClient initialized)
    ToolHandler->>ToolHandler: ShouldUseACMProxy() → "jb-mc-1", true
    ToolHandler->>ACMProxyClient: ProxyRequest(cluster="jb-mc-1", apiPath="/api/v1/namespaces/stackrox/pods")
    ACMProxyClient->>ACMProxyClient: Build URL:<br/>https://cluster-proxy-user.apps.{acm-domain}/<br/>jb-mc-1/api/v1/namespaces/stackrox/pods
    ACMProxyClient->>ClusterProxyRoute: HTTPS Request with Bearer Token
    ClusterProxyRoute->>ProxyAgent: Forward request to managed cluster
    ProxyAgent->>ManagedKubeAPI: Execute on managed cluster Kubernetes API
    ManagedKubeAPI-->>ProxyAgent: Pod list from stackrox namespace
    ProxyAgent-->>ClusterProxyRoute: Response
    ClusterProxyRoute-->>ACMProxyClient: HTTP Response
    ACMProxyClient-->>ToolHandler: Parsed pod list
    ToolHandler-->>MCPServer: Tool Result
    MCPServer-->>Client: ✅ StackRox pods from jb-mc-1 cluster

    Note over Client: SUCCESS: admission-control, collector, scanner,<br/>sensor pods listed from managed cluster
```

### Technical Implementation Details

**✅ Working Components:**
- **External Route**: `cluster-proxy-user.apps.{acm-domain}` (dynamically discovered)
- **URL Pattern**: `https://{discovered-route}/{cluster}{kubernetesApiPath}`
- **Authentication**: Standard Kubernetes bearer tokens
- **Proxy Infrastructure**: cluster-proxy-proxy-agent pods running on managed clusters
- **Route Discovery**: Automatic discovery via OpenShift Route API

**✅ Verified Operations:**
- Pod listing across namespaces ✅
- Multi-cluster resource access ✅
- Real-time API responses ✅
- Transparent authentication ✅

**🎯 Result**: Enterprise-grade multi-cluster management through single MCP interface