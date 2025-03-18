# k8s-rotator

## Motivation

Create an operator that creates a history of the states of a k8s resource.

## Architecture

We have a source resource:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: key-latest
annotations:
  rotator.k8s.telekom.com/rotation-set: my-key
  rotator.k8s.telekom.com/source: true
data:
  someKey: someValue

```
It is annotated with two labels:
- `rotator.k8s.telekom.com/rotation-set`: Specifies the rotation set this resource belongs to
- `rotator.k8s.telekom.com/source`: Marks the resource as the source for this rotation set

Optional labels include:
- `rotator.k8s.telekom.com/watch-path`: allows for watching a specific path in the resource (e.g. `.data.someKey`)
  Default is the whole resource.
- `rotator.k8s.telekom.com/rotate-path`: allows for only rotating contents in a specific path (e.g. everything in `.data`)
  Default is the same as `watch-path`.

We introduce a CRD, that is used to indicate a target resource.

```yaml
# example target 1
apiVersion: rotator.k8s.telekom.com/v1
kind: RotationTarget
metadata:
  name: key-live
spec:
  rotationSet: my-key
  rotationOrder: 1
  rotationDelay: 1h
  deletionMode: keep
```

```yaml
# example target 2
apiVersion: rotator.k8s.telekom.com/v1
kind: RotationTarget
metadata:
  name: key-fallback
spec:
  rotationSet: my-key
  rotationOrder: 2
  rotationDelay: 5m
  deletionMode: cascade
```

The operator uses the CRD to create a new resource with the same content as the source.

If the source changes, the following happens:
- Content of *example target 1* is copied to *example target 2* 5m after source was updated
- Content of source is copied to *example target 1* 1h after source was updated

In general:
- The content of the source is copied into the target with the lowest order.
- The content of a target is copied into the resource with the next highest order.
- The rotation into a target happens only after the specified delay from the time of the source change.
- The content of the target with the highest order is discarded after the new content was rotated into it.

## Open Questions

- How do we handle the state, when the number of historical states < the number of rotationTargets?
- How do we handle the case, that delay of a target with a lower order is smaller than one with a higher order?
  In that case we would have to somehow keep track of the content of the first target
- How do we handle the case, that the source is deleted? -> `deletionMode`
- How do we handle source changes, that occur more frequently than target delays?

