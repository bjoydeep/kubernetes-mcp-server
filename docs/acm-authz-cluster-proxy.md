```mermaid
sequenceDiagram
      participant C as kubernetes-mcp-server
      participant R as External Route
      participant P as cluster-proxy-addon-user (http)
      participant P1 as cluster-proxy-server
      participant A as proxy-agent (managed)
      participant K as Managed K8s API

      Note over C: Bearer: sha256~...
      C->>R: HTTPS + Bearer Token
      Note over R: cluster-proxy-user.apps.{domain}
      R->>P: Forward with Original Bearer
      Note over P: Hub Service (port 9092)
      P->>P1: http server forwards the request
      P1<<-A: Establish Secure Tunnel + Context. Token passed from hub to managed cluster
      Note over A: Validates token - will pass assuming same IdP
      A->>K: API Request (with bearer token creds)
      K->>A: API Response
      A->>P: Response via tunnel
      P->>R: Response
      R->>C: Final Response