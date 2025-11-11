<!--
SPDX-FileCopyrightText: 2025 Deutsche Telekom IT GmbH

SPDX-License-Identifier: Apache-2.0
-->

# k8s-tls-rotator

This operator is designed to facilitate key/cert rotation for OAuth2 authorization servers based on
TLS secrets stored in Kubernetes. The secret provided by this controller can be used as the basis
for a JWK set.

## Why?

We want to use cloud native solutions like cert-manager for maintaining our TLS certificates.
However, maintaining a set of TLS keys and certs for building a JWK set is not trivial.

To allow for regular rotation of keys, we need to consider the following problems when running our
authorization server as a distributed system with multiple instances:
- The server needs to continue serving the old key even after JWTs are signed with the new key, to
  continue allowing clients to validate old tokens.
- When using volume mounts to provide the keys to the authorization server, we need to consider the
  time it takes for mount propagation to take place. As this will happen with eventual consistency,
  we can't ensure that all pods serve the new key in time to allow for a smooth transition.

Because of these problems, we always need the authorization server to serve a total of three keys in
the JWK set:
- The previous key, which needs to be available to verify requests that were signed before the
  rotation
- The active key, which is used to sign tokens
- The next key, which will be the next active key and is already served to account for the mounting
  delay in the next rotation

This operator allows us to do this, while still enjoying the auto renewal capabilities of cert-manager.

## Architecture

![Architecture Diagram](./docs/architecture.svg)

### Key Rotation Process

The operator watches source secrets with the following annotations:

- `rotator.gw.ei.telekom.de/source-secret: "true"` - Marks the secret as a source
- `rotator.gw.ei.telekom.de/destination-secret-name: <name>` - Specifies the target secret name

When a source secret is detected, the operator creates a target secret with the following structure:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: target-secret
type: kubernetes.io/tls
data:
  prev-tls.crt: xxx
  prev-tls.key: xxx
  prev-tls.kid: xxx
  tls.crt: ""
  tls.key: ""
  tls.kid: ""
  next-tls.crt: ""
  next-tls.key: ""
  next-tls.kid: ""
```

**Initial creation:** The source certificate and key are placed in `next-tls.*` fields. The `next-tls.kid` contains a UUID generated from the certificate hash, which can be used as a Key ID in JWK sets. The `tls.*` and `prev-tls.*` fields are initially empty.

**Subsequent rotations:** When the source secret is updated (e.g., by cert-manager renewal), the operator performs a three-way rotation:

1. `tls.*` → `prev-tls.*` (current becomes previous)
2. `next-tls.*` → `tls.*` (next becomes current)
3. source → `next-tls.*` (new certificate becomes next)

**Important behaviors:**
- Rotation is triggered on every source secret change
- Rotation is skipped if the source certificate matches `next-tls.crt` (idempotency)
- Multiple source secrets can target the same destination (each change in one of the secrets will trigger a rotation),
  however this is discouraged because of complexity

The [integration tests](./internal/controller/secret_controller_test.go) serve as a detailed specification of the controller's behavior.

### Usage by Authorization Servers

Authorization servers (in the case of Stargate, the [issuer-service](https://github.com/telekom/gateway-issuer-service-go)) consuming the target secret should follow these rules:

- **Always expose all three keys** (`prev-tls.*`, `tls.*`, `next-tls.*`) in the JWK set
- **Always use `tls.*` for signing** new JWTs

This approach ensures:
- Resource servers can verify tokens signed with the previous key
- The next key is pre-distributed before it becomes active
- Rotation works smoothly despite eventual consistency in volume mount propagation

## Development

### Local Development Setup

Local development is easiest using a [Kind](https://kind.sigs.k8s.io/) cluster. Make sure your `kubectl` context points to your Kind cluster:

```bash
kubectl config use-context kind-<your-cluster-name>
```

### Quick Build and Deploy

Build, load, and deploy the operator in one command:

```bash
make docker-build IMG=rotator && kind load docker-image rotator && make deploy IMG=rotator
```

This builds a Docker image locally, loads it into Kind, and deploys the operator.

### Verify the Operator

Create a test source secret to verify the operator is working:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: source
  annotations:
    rotator.gw.ei.telekom.de/source-secret: "true"
    rotator.gw.ei.telekom.de/destination-secret-name: target
type: kubernetes.io/tls
stringData:
  tls.key: test-key
  tls.crt: test-crt
EOF
```

Watch the operator logs and verify it creates the target secret:

```bash
# Watch operator logs
kubectl logs -n tls-rotator-system -l control-plane=controller-manager -f

# Verify target secret was created
kubectl get secret target -o yaml
```

## Deployment

**Note:** Unlike other components in the o28m ecosystem which are deployed via Helm charts, this operator uses [Kustomize](https://kustomize.io/) for deployment configuration. Kustomize is the standard tool for Kubernetes operators built with Kubebuilder.

### Building and Pushing the Image

First, build and push your operator image to a container registry:

```bash
# Build the image
make docker-build IMG=<your-registry>/k8s-tls-rotator:tag

# Push to registry
make docker-push IMG=<your-registry>/k8s-tls-rotator:tag
```

### Deployment Options

The operator can be deployed in two modes:

#### Cluster-wide Deployment

Deploy the operator with cluster-wide permissions to watch secrets across all namespaces:

```bash
kubectl apply -k config/overlays/clusterwide
```

#### Namespace-scoped Deployment

For a more restricted deployment that only watches specific namespaces, use the namespaced overlay:

```bash
kubectl apply -k config/overlays/namespaced
```

This creates namespace-scoped roles and bindings instead of cluster-wide permissions and is useful
for deploying to shared clusters. It will automatically only watch the namespace it's deployed to.

### Configuring Custom Namespace Watching

If required, the operator supports configuring multiple namespaces:

**Via command-line flag:**
```bash
# Edit the deployment to add --namespaces flag
--namespaces=namespace1,namespace2
```

**Via environment variable:**
```yaml
env:
  - name: ROTATOR_NAMESPACES
    value: "namespace1,namespace2"
```

This is not possible using the included kustomize overlays, you would have to define your own.
If neither is set, the operator watches all namespaces (used by the  cluster-wide overlay).

### Verifying Deployment

After deployment, verify the operator is running:

```bash
# Check operator pod
kubectl get pods -n '<your-namespace>'

# Check operator logs
kubectl logs -n '<your-namespace>' -l control-plane=controller-manager
```

### Creating Source Secrets

**Production Usage:** In a real deployment, source secrets should be created and managed by [cert-manager](https://cert-manager.io/). Configure your Certificate resource to include the required annotations:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: stargate-jwk-cert
  namespace: default
spec:
  secretName: stargate-jwk-source
  secretTemplate:
    annotations:
      rotator.gw.ei.telekom.de/source-secret: "true"
      rotator.gw.ei.telekom.de/destination-secret-name: stargate-jwk-dest
  issuerRef:
    name: your-issuer
    kind: Issuer
  privateKey:
    algorithm: RSA
    encoding: PKCS8
    rotationPolicy: Always
    size: 2048
  duration: 672h  # 4 weeks
  renewBefore: 504h  # 3 weeks
```


Cert-manager will create and automatically renew the source secret with the specified annotations,
and the rotator operator will maintain the target secret with the three-key rotation pattern.

**Testing:** For testing purposes, you can create a dummy secret manually:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-tls-source
  annotations:
    rotator.gw.ei.telekom.de/source-secret: "true"
    rotator.gw.ei.telekom.de/destination-secret-name: my-rotated-keys
type: kubernetes.io/tls
stringData:
  tls.crt: test-cert-data
  tls.key: test-key-data
```

## Testing

The project includes two types of tests:

### Unit/Integration Tests

These tests use [envtest](https://book.kubebuilder.io/reference/envtest.html) to run against a real Kubernetes API server (without requiring a full cluster). They are located in [`internal/controller`](./internal/controller).

**First-time setup:**
```bash
make setup-envtest
```

**Run all tests:**
```bash
make test
```

**Run tests with coverage (CI mode):**
```bash
make test-ci
```

**Run specific tests with Ginkgo:**
```bash
cd internal/controller
go run github.com/onsi/ginkgo/v2/ginkgo -v --focus="should create target secret"
```

### E2E Tests

End-to-end tests verify the operator can be deployed in a real cluster and that its health endpoints are accessible. These tests require a running Kind cluster.

**Prerequisites:**
```bash
# Ensure Kind cluster is running
kind get clusters | grep -q 'kind' || kind create cluster
```

**Run E2E tests:**
```bash
make test-e2e
```

**Note:** Tests expect the kubectl context `kind-kind`. To use a different cluster, modify the `clusterName` variable in [`test/e2e/e2e_suite_test.go`](./test/e2e/e2e_suite_test.go).

The E2E tests currently don't verfiy any of the actual functionality.
This is covered by the unit and integration tests.

## Additional Notes

### Kubebuilder Scaffold Removal

This operator was initially scaffolded with [Kubebuilder](https://book.kubebuilder.io/) but does not require CRDs or webhooks. The generated code for these features has been removed from the manifests, Makefile, and tests to keep the project lean.

If you need to add CRDs or webhooks in the future, either check out an early commit of this project or scaffold a new Kubebuilder project and merge the code.

## License

This project is licensed under the **Apache License 2.0**. See the [LICENSE](./LICENSES/Apache-2.0.txt) file for details.

The project is [REUSE](https://reuse.software/) compliant, meaning all files have clear copyright and licensing information. REUSE compliance is verified through CI and configured in [`REUSE.toml`](./REUSE.toml).

