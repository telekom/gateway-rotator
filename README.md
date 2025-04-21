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

### Key rotation Process

The operator is configured to reconcile a set of source secrets with the following annotations.
- `rotator.gateway.mdw.telekom.de/source-secret`: A boolean value indicating whether this secret is
a source secret
- `rotator.gateway.mdw.telekom.de/destination-secret-name`: The name of the target secret to be
created

It will then create a target secret with the following structure:

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

`prev-tls.crt` and `prev-tls.key` now contain the key and cert from the source secret. The
`prev-tls.kid` is a UUID based on a hash of the certificate and can be used as a KID in the JWK set.
The other two sets of keys are currently empty and will subsequently be filled with the next
rotation.

Once the source secret is updated with new values (e.g. by cert-manager), the operator will rotate
the values:
1. `tls.*` will be written to `next-tls.*`
2. `prev-tls.*` will be written to `tls.*`
3. The values from source will be written to `prev-tls.*`

This happens on every change of the source.

If a different source secret is created with the same target annotation, the operator will also
trigger a rotation. If the values in the source are equal to the values in `prev-tls.*` the rotation
will be skipped.

[The secret controllers integration tests](./internal_controller/secret_controller_test.go) serve as
a detailed specification of the controller's behavior.

### Usage of certs/keys

The authorization server should ***always*** expose all three keys in the JWK set.

The values in `tls.*` should ***always*** be used for signing JWTs. This ensures that
- resource servers are able to verify tokens signed with the previous key
- the previously active key stays in the JWK set, ensuring verification is possible even with
  mounting delays.

## Development

Local development is easiest done using a local [Kind](https://kind.sigs.k8s.io/) cluster.

You can build and deploy changes to the operator in one step using the following command:
```bash
make docker-build IMG=rotator && kind load docker-image rotator && make deploy IMG=rotator`
```
This will build a docker image locally, load it into kind and deploy the operator into the `tls-rotator` namespace.
Make sure you have set your `kubectl` context to use your kind cluster using `kubectl config use context kind-${your-cluster-name}`.

You can then create a test secret using the following command:
```bash
cat <<EOF > secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: source
  annotations:
    rotator.gateway.mdw.telekom.de/destination-secret-name: target
    rotator.gateway.mdw.telekom.de/source-secret: "true"
type: kubernetes.io/tls
stringData:
  tls.key: test-key
  tls.crt: test-crt
EOF

kubectl apply -f secret.yaml
```
and watch the operator reconcile it.

## Testing

There are two types of tests in this project

### Unit/Integration tests

You can find these next to the controller in [`internal/controller`](./internal/controller).
On first run you need to set up envtest on your local machine to be able to construct a test environment.
```bash
make setup-envtest
```

You can then run the tests using either `make test`, or the `ginkgo` binary from the  `internal/controller` directory.

### E2E tests

These are tests generated by kubebuilder to test the operator in a real cluster.
They test if the operator is able to deploy using the [manifests](./config) and
the basic availability of the manager API and the metrics endpoint.

In the future we could add additional functionality to these tests.

You can run the tests using the following command:
```bash
make test-e2e
```
The tests are expected to run against the kubectl context called `kind-kind`.
You can change this by changing the variable `clusterName` in the e2e suite.

## Additional notes

The code generated by kubebuilder required for setting up CRDs or webhooks
has been deleted in the manifests, Makefile, tests, etc., because it is not needed in the current scope of the operator.
If you want to add any of these things in the future, we suggest either checking out an early commit of this project
or scaffolding a new project and adding them back.

